package workspacecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charlesnpx/feature-implement/internal/workspace"
	witnessreview "github.com/charlesnpx/witness/contract/review"
)

const (
	reviewRunInvocationSchema = "feature-review-run-v1"
	maxReviewAdapterOutput    = 4 * workspace.MaxArtifactBytes
	failedReviewOutputID      = "review-result"
)

// ReviewRunView is the host's observation of one adapter subprocess and the
// terminal gate record made from that observation. The report artifact paths
// are operational observations; the durable gate retains the completion
// document and its digest, not these external paths.
type ReviewRunView struct {
	DispatchDigest   string                           `json:"dispatch_digest"`
	GateRecordDigest string                           `json:"gate_record_digest"`
	Verdict          string                           `json:"verdict"`
	ExitCode         int                              `json:"exit_code"`
	RequestDigest    string                           `json:"request_digest"`
	CompletionDigest string                           `json:"completion_digest"`
	ReportArtifacts  map[string]ReviewRunArtifactView `json:"report_artifacts,omitempty"`
	Observation      string                           `json:"observation,omitempty"`
}

// ReviewRunArtifactView exposes the path and digest the host observed for a
// required report output. It does not make the output a review finding.
type ReviewRunArtifactView struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type reviewRunInvocation struct {
	SchemaVersion             string `json:"schema_version"`
	WorkspaceID               string `json:"workspace_id"`
	AttemptID                 string `json:"attempt_id"`
	DispatchDigest            string `json:"dispatch_digest"`
	Adapter                   string `json:"adapter"`
	Recipe                    string `json:"recipe"`
	PolicyDigest              string `json:"policy_digest"`
	Policy                    string `json:"policy"`
	Head                      string `json:"head"`
	Tree                      string `json:"tree"`
	FrozenCopy                string `json:"frozen_copy"`
	OutputDirectory           string `json:"output_directory"`
	ReviewConfigurationSource string `json:"review_configuration_source"`
	ReviewConfigurationDigest string `json:"review_configuration_digest"`
	ReviewConfigurationPath   string `json:"review_configuration_path,omitempty"`
	ReviewConfigurationBytes  []byte `json:"review_configuration_bytes,omitempty"`
}

type reviewRunOutput struct {
	Request         *witnessreview.ReviewRequestV2Document  `json:"request,omitempty"`
	Completion      *witnessreview.ReviewCompletionDocument `json:"completion,omitempty"`
	ReportArtifacts map[string]reviewRunArtifact            `json:"report_artifacts,omitempty"`
}

type reviewRunArtifact struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type reviewRunProcess struct {
	exitCode int
	started  bool
	stdout   []byte
	stderr   []byte
	err      error
}

func executeReviewRun(
	ctx context.Context,
	bundle workspace.WorkspaceBundle,
	options Options,
) (any, error) {
	var input dispatchReviewGateInput
	if err := decodeRequest(options.Input, &input); err != nil {
		return nil, err
	}
	occurredAt, err := parseOccurredAt(input.SchemaVersion, input.OccurredAt)
	if err != nil {
		return nil, err
	}
	attemptID, err := parseID(input.AttemptID, "attempt_id")
	if err != nil {
		return nil, err
	}

	journal, _, err := openWritableJournal(options)
	if err != nil {
		return nil, err
	}
	defer journal.Close()

	definition := bundle.Definition()
	repository := localReviewRepository{git: workspace.DefaultLocalCommitGitAdapter()}
	dispatched, err := workspace.DispatchAttemptReviewGate(
		ctx,
		journal,
		definition,
		repository,
		workspace.DefaultLocalAttemptGitAdapter(),
		workspace.ReviewGateDispatchRequest{AttemptID: attemptID, OccurredAt: occurredAt},
	)
	if err != nil {
		return nil, err
	}

	configuration := bundle.ReviewConfiguration()
	dispatchedConfiguration := dispatched.ReviewConfiguration()
	if configuration.Source() != dispatchedConfiguration.Source() ||
		configuration.Digest() != dispatchedConfiguration.Digest() {
		return nil, fmt.Errorf("review run configuration does not match the frozen workspace bundle")
	}

	outputDirectory, err := os.MkdirTemp("", "feature-implement-review-run-")
	if err != nil {
		return nil, fmt.Errorf("create review adapter output directory: %w", err)
	}
	defer os.RemoveAll(outputDirectory)
	outputDirectory, err = filepath.EvalSymlinks(outputDirectory)
	if err != nil {
		return nil, fmt.Errorf("resolve review adapter output directory: %w", err)
	}
	configurationPath, err := materializeReviewConfiguration(outputDirectory, configuration)
	if err != nil {
		return nil, err
	}

	invocation := reviewRunInvocation{
		SchemaVersion:             reviewRunInvocationSchema,
		WorkspaceID:               dispatched.Dispatch().WorkspaceID().String(),
		AttemptID:                 dispatched.Dispatch().AttemptID().String(),
		DispatchDigest:            dispatched.Dispatch().Digest().String(),
		Adapter:                   dispatched.Dispatch().Adapter().String(),
		Recipe:                    dispatched.Dispatch().Recipe().String(),
		PolicyDigest:              dispatched.Dispatch().PolicyDigest().String(),
		Policy:                    string(dispatched.Policy()),
		Head:                      dispatched.Dispatch().Head().String(),
		Tree:                      dispatched.Dispatch().Tree().String(),
		FrozenCopy:                dispatched.FrozenCopy(),
		OutputDirectory:           outputDirectory,
		ReviewConfigurationSource: configuration.Source(),
		ReviewConfigurationDigest: configuration.Digest().String(),
		ReviewConfigurationPath:   configurationPath,
		ReviewConfigurationBytes:  configuration.Bytes(),
	}
	invocationBytes, err := json.Marshal(invocation)
	if err != nil {
		return nil, fmt.Errorf("encode review adapter invocation: %w", err)
	}
	process := runReviewAdapter(ctx, dispatched.Dispatch().Adapter().String(), dispatched.FrozenCopy(), invocationBytes)

	output, outputErr := parseReviewRunOutput(process.stdout)
	expectedVerdict, recognizedExit := reviewRunExitVerdict(process.exitCode)
	var (
		request         witnessreview.ReviewRequestV2Document
		completion      witnessreview.ReviewCompletionDocument
		evidence        witnessreview.HostExecutionEvidence
		observed        witnessreview.ObservedReviewExecution
		reportArtifacts map[string]ReviewRunArtifactView
		observation     string
	)

	if process.started && recognizedExit && outputErr == nil && output.Request != nil && output.Completion != nil &&
		output.Completion.Verdict == expectedVerdict && reviewRunCompletionMatchesRequest(*output.Request, *output.Completion) {
		request = *output.Request
		observed, reportArtifacts = observeReviewRunArtifacts(
			request,
			output.Completion.RequiredReportDigests,
			output.ReportArtifacts,
			dispatched.FrozenCopy(),
			outputDirectory,
			expectedVerdict,
		)
		evidence, err = witnessreview.NewHostExecutionEvidence(observed)
		if err == nil {
			completion, err = witnessreview.NewReviewCompletionDocument(
				request,
				evidence,
				cloneReviewReportDigests(output.Completion.RequiredReportDigests),
				expectedVerdict,
			)
		}
		if err != nil {
			observation = fmt.Sprintf("adapter output could not be recorded as observed completion: %v", err)
		}
	} else {
		observation = reviewRunFailureObservation(process, outputErr, expectedVerdict, recognizedExit)
	}

	if completion.SchemaVersion == "" {
		// A missing process, unknown exit code, malformed/no stdout completion, or
		// any unusable completion is failed_to_run. The synthetic request is a
		// failure-only binding; it is never allowed to claim satisfaction.
		request, err = failedReviewRequest(dispatched.Dispatch())
		if err != nil {
			return nil, fmt.Errorf("construct failed-to-run review request: %w", err)
		}
		observed = failedReviewExecution()
		reportArtifacts = nil
		evidence, err = witnessreview.NewHostExecutionEvidence(observed)
		if err != nil {
			return nil, fmt.Errorf("construct failed-to-run host evidence: %w", err)
		}
		completion, err = witnessreview.NewReviewCompletionDocument(
			request, evidence, map[string]string{}, witnessreview.CompletionVerdictFailedToRun,
		)
		if err != nil {
			return nil, fmt.Errorf("construct failed-to-run review completion: %w", err)
		}
	}

	requestDigest, err := witnessreview.ReviewRequestV2Digest(request)
	if err != nil {
		return nil, fmt.Errorf("digest observed review request: %w", err)
	}
	completionDigest, err := witnessreview.ReviewCompletionDigest(completion)
	if err != nil {
		return nil, fmt.Errorf("digest observed review completion: %w", err)
	}
	recorded, _, err := workspace.RecordAttemptReviewCompletion(
		journal,
		definition,
		workspace.RecordAttemptReviewCompletionRequest{
			AttemptID: attemptID, DispatchDigest: dispatched.Dispatch().Digest(),
			Request: request, Completion: completion, OccurredAt: occurredAt,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("record review run completion: %w", err)
	}

	detail := ReviewRunView{
		DispatchDigest:   dispatched.Dispatch().Digest().String(),
		GateRecordDigest: recorded.GateRecord().Digest().String(),
		Verdict:          string(recorded.GateRecord().Verdict()),
		ExitCode:         process.exitCode,
		RequestDigest:    requestDigest,
		CompletionDigest: completionDigest,
		ReportArtifacts:  reportArtifacts,
		Observation:      observation,
	}
	return reviewCommandResult("review.run", detail, journal, definition)
}

func materializeReviewConfiguration(outputDirectory string, configuration workspace.ReviewConfiguration) (string, error) {
	if configuration.BundledDefault() {
		return "", nil
	}
	path := filepath.Join(outputDirectory, "review-configuration.json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create frozen review configuration: %w", err)
	}
	content := configuration.Bytes()
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write frozen review configuration: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("synchronize frozen review configuration: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close frozen review configuration: %w", err)
	}
	stored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, content) {
		if err == nil {
			err = errors.New("stored bytes differ from the frozen bundle bytes")
		}
		return "", fmt.Errorf("verify frozen review configuration: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve frozen review configuration: %w", err)
	}
	return resolved, nil
}

func runReviewAdapter(ctx context.Context, adapter, frozenCopy string, invocation []byte) reviewRunProcess {
	command := exec.CommandContext(ctx, adapter, "review", "run")
	command.Dir = frozenCopy
	command.Stdin = bytes.NewReader(invocation)
	stdout := &boundedReviewOutput{limit: maxReviewAdapterOutput}
	stderr := &boundedReviewOutput{limit: maxReviewAdapterOutput}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	process := reviewRunProcess{
		exitCode: -1, started: true, stdout: stdout.Bytes(), stderr: stderr.Bytes(), err: err,
	}
	var exitErr *exec.ExitError
	if err == nil {
		process.exitCode = 0
		return process
	}
	if errors.As(err, &exitErr) {
		process.exitCode = exitErr.ExitCode()
		return process
	}
	process.started = false
	return process
}

type boundedReviewOutput struct {
	bytes.Buffer
	limit int
}

func (output *boundedReviewOutput) Write(value []byte) (int, error) {
	remaining := output.limit - output.Len()
	if remaining <= 0 {
		return 0, fmt.Errorf("review adapter output exceeds %d bytes", output.limit)
	}
	if len(value) > remaining {
		_, _ = output.Buffer.Write(value[:remaining])
		return remaining, fmt.Errorf("review adapter output exceeds %d bytes", output.limit)
	}
	return output.Buffer.Write(value)
}

func parseReviewRunOutput(source []byte) (reviewRunOutput, error) {
	if len(bytes.TrimSpace(source)) == 0 {
		return reviewRunOutput{}, errors.New("review adapter produced no parseable completion on stdout")
	}
	var output reviewRunOutput
	if err := workspace.DecodeStrictJSON(source, &output); err != nil {
		return reviewRunOutput{}, fmt.Errorf("decode review adapter stdout completion: %w", err)
	}
	if output.Request == nil || output.Completion == nil {
		return reviewRunOutput{}, errors.New("review adapter stdout must contain request and completion")
	}
	return output, nil
}

func reviewRunCompletionMatchesRequest(
	request witnessreview.ReviewRequestV2Document,
	completion witnessreview.ReviewCompletionDocument,
) bool {
	if completion.SchemaVersion != witnessreview.ReviewCompletionV1 || completion.Adapter != request.Adapter {
		return false
	}
	requestDigest, err := witnessreview.ReviewRequestV2Digest(request)
	return err == nil && completion.RequestDigest == requestDigest
}

func reviewRunExitVerdict(exitCode int) (string, bool) {
	// The adapter protocol reserves 0 for satisfied, 20 for not_satisfied,
	// and 21 for failed_to_run. Startup errors, unknown exits, and unusable
	// stdout completions are host-observed failed_to_run outcomes.
	switch exitCode {
	case 0:
		return witnessreview.CompletionVerdictSatisfied, true
	case 20:
		return witnessreview.CompletionVerdictNotSatisfied, true
	case 21:
		return witnessreview.CompletionVerdictFailedToRun, true
	default:
		return "", false
	}
}

func reviewRunFailureObservation(
	process reviewRunProcess,
	outputErr error,
	expectedVerdict string,
	recognizedExit bool,
) string {
	parts := make([]string, 0, 3)
	if !process.started {
		parts = append(parts, "review adapter subprocess could not be started")
	}
	if !recognizedExit {
		parts = append(parts, fmt.Sprintf("review adapter exited with unknown code %d", process.exitCode))
	} else if expectedVerdict != "" {
		parts = append(parts, fmt.Sprintf("review adapter exit maps to %s", expectedVerdict))
	}
	if outputErr != nil {
		parts = append(parts, outputErr.Error())
	}
	if process.err != nil && process.started && !recognizedExit {
		parts = append(parts, process.err.Error())
	}
	if len(bytes.TrimSpace(process.stderr)) != 0 {
		parts = append(parts, fmt.Sprintf("stderr: %s", strings.TrimSpace(string(process.stderr))))
	}
	if len(parts) == 0 {
		return "review adapter completion was unusable; recorded failed_to_run"
	}
	return strings.Join(append(parts, "recorded failed_to_run"), "; ")
}

func observeReviewRunArtifacts(
	request witnessreview.ReviewRequestV2Document,
	reportDigests map[string]string,
	locations map[string]reviewRunArtifact,
	frozenCopy string,
	outputDirectory string,
	verdict string,
) (witnessreview.ObservedReviewExecution, map[string]ReviewRunArtifactView) {
	observed := witnessreview.ObservedReviewExecution{
		Complete:                verdict != witnessreview.CompletionVerdictFailedToRun,
		ResultArtifactAvailable: true,
		ReportOutcomes:          make(map[string]witnessreview.ObservedReportOutcome, len(request.RequiredOutputs)),
	}
	views := make(map[string]ReviewRunArtifactView, len(request.RequiredOutputs))
	for _, reviewer := range request.RequiredOutputs {
		location, exists := locations[reviewer]
		if !exists || strings.TrimSpace(location.Path) == "" {
			observed.ReportOutcomes[reviewer] = witnessreview.ObservedReportOutcome{Status: witnessreview.ExecutionReportMissing}
			observed.ResultArtifactAvailable = false
			continue
		}
		view := ReviewRunArtifactView{Path: location.Path, Digest: location.Digest}
		resolved, data, err := readReviewRunArtifact(location.Path, frozenCopy, outputDirectory)
		if err != nil {
			observed.ReportOutcomes[reviewer] = witnessreview.ObservedReportOutcome{Status: witnessreview.ExecutionReportUnavailable}
			observed.ResultArtifactAvailable = false
			views[reviewer] = view
			continue
		}
		actual := workspace.DigestBytes(data).String()
		view.Path = resolved
		view.Digest = actual
		if actual != location.Digest || actual != reportDigests[reviewer] {
			observed.ReportOutcomes[reviewer] = witnessreview.ObservedReportOutcome{Status: witnessreview.ExecutionReportUnavailable}
			observed.ResultArtifactAvailable = false
			views[reviewer] = view
			continue
		}
		observed.ReportOutcomes[reviewer] = witnessreview.ObservedReportOutcome{Status: witnessreview.ExecutionReportValid}
		views[reviewer] = view
	}
	return observed, views
}

func readReviewRunArtifact(rawPath, frozenCopy, outputDirectory string) (string, []byte, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", nil, errors.New("review artifact path is required")
	}
	candidates := []string{}
	if filepath.IsAbs(rawPath) {
		candidates = append(candidates, rawPath)
	} else {
		candidates = append(candidates, filepath.Join(outputDirectory, rawPath), filepath.Join(frozenCopy, rawPath))
	}
	var lastErr error
	for _, candidate := range candidates {
		resolved, err := filepath.EvalSymlinks(filepath.Clean(candidate))
		if err != nil {
			lastErr = err
			continue
		}
		file, err := os.Open(resolved)
		if err != nil {
			lastErr = err
			continue
		}
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			lastErr = statErr
			continue
		}
		if !info.Mode().IsRegular() || info.Size() > int64(workspace.MaxArtifactBytes) {
			_ = file.Close()
			lastErr = fmt.Errorf("review artifact is not a bounded regular file")
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(file, int64(workspace.MaxArtifactBytes)+1))
		closeErr := file.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if closeErr != nil {
			lastErr = closeErr
			continue
		}
		if len(data) > workspace.MaxArtifactBytes {
			lastErr = fmt.Errorf("review artifact exceeds %d bytes", workspace.MaxArtifactBytes)
			continue
		}
		return resolved, data, nil
	}
	if lastErr == nil {
		lastErr = errors.New("review artifact could not be resolved")
	}
	return "", nil, lastErr
}

func failedReviewRequest(dispatch workspace.ReviewGateDispatch) (witnessreview.ReviewRequestV2Document, error) {
	recipe := witnessreview.ReviewRecipe{
		RecipeID:        dispatch.Recipe().String(),
		Instructions:    "The configured review adapter did not produce a parseable completion record.",
		RequiredOutputs: []string{failedReviewOutputID},
		Policy:          map[string]any{},
	}
	recipeBytes, err := witnessreview.ReviewRecipeCanonicalBytes(recipe)
	if err != nil {
		return witnessreview.ReviewRequestV2Document{}, err
	}
	recipeDigest, err := witnessreview.ReviewRecipeDigest(recipeBytes)
	if err != nil {
		return witnessreview.ReviewRequestV2Document{}, err
	}
	marker := dispatch.Digest().String()
	request := witnessreview.ReviewRequestV2Document{
		SchemaVersion: witnessreview.ReviewRequestV2,
		ConsumerIdentity: witnessreview.Identity{
			Kind: "feature-implement", ID: dispatch.WorkspaceID().String(),
		},
		Subject: witnessreview.RequestSubject{
			Head: dispatch.Head().String(), Tree: dispatch.Tree().String(),
		},
		CharterHash:       workspace.DigestBytes([]byte("feature-implement-review-failure-charter:" + marker)).String(),
		ReviewInputDigest: workspace.DigestBytes([]byte("feature-implement-review-failure-input:" + marker)).String(),
		FrozenRecipe:      recipeBytes,
		RecipeDigest:      recipeDigest,
		Adapter:           dispatch.Adapter().String(),
		RequiredOutputs:   []string{failedReviewOutputID},
	}
	return request, nil
}

func failedReviewExecution() witnessreview.ObservedReviewExecution {
	return witnessreview.ObservedReviewExecution{
		Complete: false, ResultArtifactAvailable: false,
		ReportOutcomes: map[string]witnessreview.ObservedReportOutcome{
			failedReviewOutputID: {Status: witnessreview.ExecutionReportMissing},
		},
	}
}

func cloneReviewReportDigests(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for reviewer, digest := range source {
		result[reviewer] = digest
	}
	return result
}
