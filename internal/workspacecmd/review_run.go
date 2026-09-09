package workspacecmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charlesnpx/feature-implement/internal/workspace"
	"github.com/charlesnpx/witness/contract/charter"
	witnessreview "github.com/charlesnpx/witness/contract/review"
	"github.com/charlesnpx/witness/contract/strictjson"
)

const (
	maxReviewAdapterOutput = 4 * workspace.MaxArtifactBytes
	failedReviewOutputID   = "review-result"
)

// ReviewRunView is the host's observation of one adapter subprocess and the
// terminal gate record made from that observation.
type ReviewRunView struct {
	DispatchDigest   string `json:"dispatch_digest"`
	GateRecordDigest string `json:"gate_record_digest"`
	Verdict          string `json:"verdict"`
	ExitCode         int    `json:"exit_code"`
	RequestDigest    string `json:"request_digest"`
	CompletionDigest string `json:"completion_digest"`
	Observation      string `json:"observation,omitempty"`
}

type reviewRunSummary struct {
	OK                bool                  `json:"ok"`
	Verdict           string                `json:"verdict"`
	RequestPath       string                `json:"request_path"`
	CharterFreezePath string                `json:"charter_freeze_path"`
	CompletionPath    string                `json:"completion_path"`
	Jobs              []reviewRunJobSummary `json:"jobs"`
	Diagnostics       []string              `json:"diagnostics,omitempty"`
}

type reviewRunJobSummary struct {
	Reviewer                string `json:"reviewer"`
	JobID                   string `json:"job_id"`
	State                   string `json:"state"`
	StateExitCode           int    `json:"state_exit_code"`
	ReportStatus            string `json:"report_status"`
	ResultArtifactAvailable bool   `json:"result_artifact_available"`
	TranscriptComplete      bool   `json:"transcript_complete"`
	TranscriptGap           bool   `json:"transcript_gap"`
	ResultError             string `json:"result_error,omitempty"`
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
	if strings.TrimSpace(options.CharterPath) == "" {
		return nil, fmt.Errorf("workspace review run requires --charter <path>")
	}
	charterPath, err := resolveReviewCharterPath(options.CharterPath)
	if err != nil {
		return nil, fmt.Errorf("resolve supplied Charter path: %w", err)
	}
	charterHash, err := reviewCharterHash(charterPath)
	if err != nil {
		return nil, fmt.Errorf("compute supplied Charter hash: %w", err)
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

	process := runReviewAdapter(
		ctx,
		dispatched.Dispatch().Adapter().String(),
		dispatched.FrozenCopy(),
		outputDirectory,
		configurationPath,
		dispatched.Dispatch().Head().String(),
		dispatched.Dispatch().Tree().String(),
		dispatched.Dispatch().WorkspaceID().String(),
		charterPath,
	)

	summary, summaryErr := parseReviewRunSummary(process.stdout)
	expectedVerdict, recognizedExit := reviewRunExitVerdict(process.exitCode)
	var (
		request     witnessreview.ReviewRequestV2Document
		completion  witnessreview.ReviewCompletionDocument
		evidence    witnessreview.HostExecutionEvidence
		observed    witnessreview.ObservedReviewExecution
		observation string
	)

	requestBytes, requestReadErr := readReviewRunDocument(outputDirectory, "review-request.json")
	completionBytes, completionReadErr := readReviewRunDocument(outputDirectory, "review-completion.json")
	var (
		decodedRequest      witnessreview.ReviewRequestV2Document
		requestDecodeErr    error
		adapterCompletion   witnessreview.ReviewCompletionDocument
		completionDecodeErr error
	)
	if requestReadErr == nil {
		decodedRequest, requestDecodeErr = witnessreview.DecodeAndValidateReviewRequestV2(requestBytes)
	}
	if completionReadErr == nil {
		adapterCompletion, completionDecodeErr = witnessreview.DecodeReviewCompletion(completionBytes)
	}
	requestDocumentErr := requestReadErr
	if requestDocumentErr == nil {
		requestDocumentErr = requestDecodeErr
	}
	if requestDocumentErr == nil && !reviewRunRequestMatchesDispatch(decodedRequest, dispatched.Dispatch(), charterHash) {
		requestDocumentErr = errors.New("review request does not match the review gate dispatch or supplied Charter")
	}
	if requestDocumentErr == nil {
		request = decodedRequest
	}
	completionDocumentErr := completionReadErr
	if completionDocumentErr == nil {
		completionDocumentErr = completionDecodeErr
	}
	if process.started && recognizedExit && summaryErr == nil &&
		summary.Verdict == expectedVerdict && summary.OK == (expectedVerdict == witnessreview.CompletionVerdictSatisfied) &&
		requestDocumentErr == nil && completionDocumentErr == nil {
		if adapterCompletion.Verdict != expectedVerdict || !reviewRunCompletionMatchesRequest(request, adapterCompletion) {
			err = errors.New("review completion does not match the request or adapter exit verdict")
		}
		if err == nil {
			observed, err = observeReviewRunSummary(request, summary, expectedVerdict)
		}
		if err == nil {
			evidence, err = witnessreview.NewHostExecutionEvidence(observed)
		}
		if err == nil {
			completion, err = witnessreview.NewReviewCompletionDocument(
				request,
				evidence,
				adapterCompletion.RequiredReportDigests,
				expectedVerdict,
			)
		}
		if err != nil {
			observation = fmt.Sprintf("adapter output documents could not be recorded as observed completion: %v", err)
		}
	} else {
		observation = reviewRunFailureObservation(
			process,
			summaryErr,
			expectedVerdict,
			recognizedExit,
			requestDocumentErr,
			completionDocumentErr,
		)
	}

	if completion.SchemaVersion == "" {
		// A missing process, unknown exit code, malformed/no stdout summary, or any
		// unusable output document is failed_to_run. Retain a valid adapter request
		// when one exists so the recorded failure still binds the supplied Charter.
		if requestDocumentErr != nil {
			request, err = failedReviewRequest(dispatched.Dispatch(), charterHash)
			if err != nil {
				return nil, fmt.Errorf("construct failed-to-run review request: %w", err)
			}
		}
		observed = failedReviewExecution(request)
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
		Observation:      observation,
	}
	return reviewCommandResult("review.run", detail, journal, definition)
}

func resolveReviewCharterPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return resolved, nil
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

func reviewCharterHash(path string) (string, error) {
	input, sourceErr := charter.ReadFile(path)
	if sourceErr == nil {
		frozen, err := charter.Freeze(input, nil)
		if err != nil {
			return "", fmt.Errorf("freeze Charter: %w", err)
		}
		return frozen.CharterHash, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Charter or frozen Charter: %w", err)
	}
	frozen, frozenErr := strictjson.DecodeBytes[charter.FrozenCharter](data, strictjson.DefaultMaxBytes)
	if frozenErr != nil {
		return "", fmt.Errorf("decode Charter or frozen Charter: Charter: %v; frozen Charter: %w", sourceErr, frozenErr)
	}
	expected, err := charter.Hash(frozen.Charter)
	if err != nil {
		return "", fmt.Errorf("hash frozen Charter: %w", err)
	}
	if frozen.CharterHash != expected {
		return "", fmt.Errorf("frozen Charter hash %q does not match its embedded Charter hash %q", frozen.CharterHash, expected)
	}
	return expected, nil
}

func runReviewAdapter(
	ctx context.Context,
	adapter string,
	frozenCopy string,
	outputDirectory string,
	configurationPath string,
	head string,
	tree string,
	workspaceID string,
	charterPath string,
) reviewRunProcess {
	arguments := []string{
		"review", "run",
		"-source-dir", frozenCopy,
		"-out-dir", outputDirectory,
	}
	if strings.TrimSpace(configurationPath) != "" {
		arguments = append(arguments, "-config", configurationPath)
	}
	arguments = append(arguments,
		"-subject-head", head,
		"-subject-tree", tree,
		"-consumer-kind", "feature-implement",
		"-consumer-id", workspaceID,
		"-charter", charterPath,
	)
	command := exec.CommandContext(ctx, adapter, arguments...)
	command.Dir = frozenCopy
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

func parseReviewRunSummary(source []byte) (reviewRunSummary, error) {
	if len(bytes.TrimSpace(source)) == 0 {
		return reviewRunSummary{}, errors.New("review adapter produced no parseable summary on stdout")
	}
	var summary reviewRunSummary
	if err := workspace.DecodeStrictJSON(source, &summary); err != nil {
		return reviewRunSummary{}, fmt.Errorf("decode review adapter stdout summary: %w", err)
	}
	if strings.TrimSpace(summary.Verdict) == "" ||
		strings.TrimSpace(summary.RequestPath) == "" ||
		strings.TrimSpace(summary.CharterFreezePath) == "" ||
		strings.TrimSpace(summary.CompletionPath) == "" ||
		summary.Jobs == nil {
		return reviewRunSummary{}, errors.New("review adapter stdout summary is missing required fields")
	}
	return summary, nil
}

func readReviewRunDocument(outputDirectory, name string) ([]byte, error) {
	root, err := filepath.EvalSymlinks(outputDirectory)
	if err != nil {
		return nil, fmt.Errorf("resolve review adapter output directory: %w", err)
	}
	path := filepath.Join(root, name)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("resolve review adapter document %q: %w", name, err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("review adapter document %q resolves outside output directory", name)
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, fmt.Errorf("open review adapter document %q: %w", name, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat review adapter document %q: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Size() > int64(workspace.MaxArtifactBytes) {
		return nil, fmt.Errorf("review adapter document %q is not a bounded regular file", name)
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(workspace.MaxArtifactBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read review adapter document %q: %w", name, err)
	}
	if len(data) > workspace.MaxArtifactBytes {
		return nil, fmt.Errorf("review adapter document %q exceeds %d bytes", name, workspace.MaxArtifactBytes)
	}
	return data, nil
}

func observeReviewRunSummary(
	request witnessreview.ReviewRequestV2Document,
	summary reviewRunSummary,
	verdict string,
) (witnessreview.ObservedReviewExecution, error) {
	observed := witnessreview.ObservedReviewExecution{
		Complete:                verdict != witnessreview.CompletionVerdictFailedToRun,
		ResultArtifactAvailable: true,
		ReportOutcomes:          make(map[string]witnessreview.ObservedReportOutcome, len(request.RequiredOutputs)),
	}
	seen := make(map[string]struct{}, len(summary.Jobs))
	for _, job := range summary.Jobs {
		reviewer := strings.TrimSpace(job.Reviewer)
		if reviewer == "" {
			return witnessreview.ObservedReviewExecution{}, errors.New("review adapter summary contains a job without a reviewer")
		}
		if _, exists := seen[reviewer]; exists {
			return witnessreview.ObservedReviewExecution{}, fmt.Errorf("review adapter summary repeats reviewer %q", reviewer)
		}
		seen[reviewer] = struct{}{}
		observed.ReportOutcomes[reviewer] = witnessreview.ObservedReportOutcome{Status: job.ReportStatus}
		if !job.ResultArtifactAvailable {
			observed.ResultArtifactAvailable = false
		}
		if job.State != "completed" && job.State != "completed_noncompliant" {
			observed.Complete = false
		}
	}
	for _, reviewer := range request.RequiredOutputs {
		if _, exists := seen[reviewer]; !exists {
			observed.ReportOutcomes[reviewer] = witnessreview.ObservedReportOutcome{Status: witnessreview.ExecutionReportMissing}
			observed.ResultArtifactAvailable = false
		}
	}
	return observed, nil
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

func reviewRunRequestMatchesDispatch(
	request witnessreview.ReviewRequestV2Document,
	dispatch workspace.ReviewGateDispatch,
	charterHash string,
) bool {
	// Adapter and recipe IDs belong to the review tool's vocabulary, not the
	// host gate labels. The frozen configuration already selects that behavior,
	// so this check binds only dispatch facts and the supplied Charter.
	return request.ConsumerIdentity.Kind == "feature-implement" &&
		request.ConsumerIdentity.ID == dispatch.WorkspaceID().String() &&
		request.Subject.Head == dispatch.Head().String() &&
		request.Subject.Tree == dispatch.Tree().String() &&
		request.CharterHash == charterHash
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
	summaryErr error,
	expectedVerdict string,
	recognizedExit bool,
	requestErr error,
	completionErr error,
) string {
	parts := make([]string, 0, 5)
	if !process.started {
		parts = append(parts, "review adapter subprocess could not be started")
	}
	if !recognizedExit {
		parts = append(parts, fmt.Sprintf("review adapter exited with unknown code %d", process.exitCode))
	} else if expectedVerdict != "" {
		parts = append(parts, fmt.Sprintf("review adapter exit maps to %s", expectedVerdict))
	}
	if summaryErr != nil {
		parts = append(parts, summaryErr.Error())
	}
	if requestErr != nil {
		parts = append(parts, requestErr.Error())
	}
	if completionErr != nil {
		parts = append(parts, completionErr.Error())
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

func failedReviewRequest(dispatch workspace.ReviewGateDispatch, charterHash string) (witnessreview.ReviewRequestV2Document, error) {
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
		CharterHash:       charterHash,
		ReviewInputDigest: workspace.DigestBytes([]byte("feature-implement-review-failure-input:" + marker)).String(),
		FrozenRecipe:      recipeBytes,
		RecipeDigest:      recipeDigest,
		Adapter:           dispatch.Adapter().String(),
		RequiredOutputs:   []string{failedReviewOutputID},
	}
	return request, nil
}

func failedReviewExecution(request witnessreview.ReviewRequestV2Document) witnessreview.ObservedReviewExecution {
	outcomes := make(map[string]witnessreview.ObservedReportOutcome, len(request.RequiredOutputs))
	for _, reviewer := range request.RequiredOutputs {
		outcomes[reviewer] = witnessreview.ObservedReportOutcome{Status: witnessreview.ExecutionReportMissing}
	}
	return witnessreview.ObservedReviewExecution{
		Complete: false, ResultArtifactAvailable: false, ReportOutcomes: outcomes,
	}
}
