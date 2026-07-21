package workspace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

func ParseCheckOutcome(parser CheckParserKind, result CheckProcessResult) (ParsedCheckOutcome, error) {
	if !parser.valid() {
		return ParsedCheckOutcome{}, fmt.Errorf("unsupported structured check parser %q", parser)
	}
	if !result.termination.valid() || result.output.IsZero() {
		return ParsedCheckOutcome{}, fmt.Errorf("check parser requires a complete process result")
	}
	if result.termination != CheckExited {
		kind := CheckOutcomeInfrastructureFailed
		switch result.termination {
		case CheckTimedOut:
			kind = CheckOutcomeTimedOut
		case CheckSignaled:
			kind = CheckOutcomeSignaled
		case CheckCrashed:
			kind = CheckOutcomeCrashed
		case CheckMissingExecutable:
			kind = CheckOutcomeMissingExecutable
		case CheckInfrastructure:
			kind = CheckOutcomeInfrastructureFailed
		}
		return NewParsedCheckOutcome(kind, nil)
	}
	if len(result.stderr) != 0 {
		return NewParsedCheckOutcome(CheckOutcomeInfrastructureFailed, nil)
	}
	switch parser {
	case CheckParserGoTestJSON:
		return parseGoTestJSON(result)
	case CheckParserAssertionJSON:
		return parseAssertionDocument(result, false)
	case CheckParserDiagnosticJSON:
		return parseAssertionDocument(result, true)
	default:
		return ParsedCheckOutcome{}, fmt.Errorf("unsupported structured check parser %q", parser)
	}
}

func GoTestFailureIdentity(packageName, testName string) (string, error) {
	packageName = strings.TrimSpace(packageName)
	testName = strings.TrimSpace(testName)
	if packageName == "" || testName == "" || strings.ContainsAny(packageName+testName, "\x00\r\n") {
		return "", fmt.Errorf("Go test failure identity requires package and test")
	}
	identity := packageName + "::" + testName
	if len(identity) > maxCheckFailureIDBytes {
		return "", fmt.Errorf("Go test failure identity exceeds its bound")
	}
	return identity, nil
}

func parseGoTestJSON(result CheckProcessResult) (ParsedCheckOutcome, error) {
	type goTestEvent struct {
		Action      string  `json:"Action"`
		Package     string  `json:"Package"`
		ImportPath  string  `json:"ImportPath"`
		Test        string  `json:"Test"`
		Output      string  `json:"Output"`
		FailedBuild string  `json:"FailedBuild"`
		Elapsed     float64 `json:"Elapsed"`
		Time        string  `json:"Time"`
	}
	type goTestPackageState struct {
		terminalAction string
		namedFailure   bool
		failedBuild    string
	}
	type goTestBuildState struct {
		failed bool
	}
	scanner := bufio.NewScanner(bytes.NewReader(result.stdout))
	scanner.Buffer(make([]byte, 64*1024), maxAttemptGitOutputBytes)
	failures := make(map[string]struct{})
	namedFailurePackages := make(map[string]struct{})
	packageFailures := make(map[string]struct{})
	packages := make(map[string]goTestPackageState)
	builds := make(map[string]goTestBuildState)
	structured := 0
	crashed, signaled, timedOut := false, false, false
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event goTestEvent
		if err := rejectDuplicateJSONObjectKeys(line); err != nil {
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
		if err := json.Unmarshal(line, &event); err != nil || event.Action == "" {
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
		structured++
		if event.Action == "build-output" || event.Action == "build-fail" {
			if event.ImportPath == "" || event.Package != "" || event.Test != "" || event.FailedBuild != "" {
				return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
			}
			buildState := builds[event.ImportPath]
			if buildState.failed {
				return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
			}
			if event.Action == "build-fail" {
				buildState.failed = true
			}
			builds[event.ImportPath] = buildState
			continue
		}
		if event.Package == "" || event.ImportPath != "" ||
			(event.FailedBuild != "" && (event.Action != "fail" || event.Test != "")) {
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
		packageState := packages[event.Package]
		if packageState.terminalAction != "" {
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
		terminal := false
		switch event.Action {
		case "start", "run", "pause", "cont", "bench":
		case "output":
			text := strings.ToLower(event.Output)
			if strings.Contains(text, "test timed out after") || strings.Contains(text, "panic: test timed out") {
				timedOut = true
			}
			if strings.Contains(text, "panic:") || strings.Contains(text, "fatal error:") ||
				strings.Contains(text, "signal: aborted") || strings.Contains(text, "signal: segmentation fault") {
				crashed = true
			}
			if event.Test == "" {
				outputSignaled, outputCrashed := goTestPackageAbnormalExit(text)
				signaled = signaled || outputSignaled
				crashed = crashed || outputCrashed
			}
		case "pass", "skip":
			if event.Test == "" {
				if packageState.namedFailure {
					return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
				}
				terminal, packageState.terminalAction = true, event.Action
			}
		case "fail":
			if event.Test != "" {
				identity, err := GoTestFailureIdentity(event.Package, event.Test)
				if err != nil {
					return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
				}
				failures[identity] = struct{}{}
				namedFailurePackages[event.Package] = struct{}{}
				packageState.namedFailure = true
			} else {
				packageFailures[event.Package] = struct{}{}
				terminal, packageState.terminalAction = true, event.Action
				if event.FailedBuild != "" {
					if packageState.namedFailure {
						return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
					}
					packageState.failedBuild = event.FailedBuild
				}
			}
		default:
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
		if !terminal {
			packageState.terminalAction = ""
		}
		packages[event.Package] = packageState
	}
	if err := scanner.Err(); err != nil || structured == 0 || len(packages) == 0 {
		return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
	}
	for _, packageState := range packages {
		if packageState.terminalAction == "" {
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
	}
	for packageName := range namedFailurePackages {
		if _, failed := packageFailures[packageName]; !failed || packages[packageName].terminalAction != "fail" ||
			packages[packageName].failedBuild != "" {
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
	}
	referencedBuilds := make(map[string]struct{})
	for _, packageState := range packages {
		if packageState.failedBuild == "" {
			continue
		}
		buildState, exists := builds[packageState.failedBuild]
		if !exists || !buildState.failed {
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
		referencedBuilds[packageState.failedBuild] = struct{}{}
	}
	for importPath, buildState := range builds {
		if buildState.failed {
			if _, referenced := referencedBuilds[importPath]; !referenced {
				return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
			}
		}
	}
	if timedOut {
		return NewParsedCheckOutcome(CheckOutcomeTimedOut, nil)
	}
	if signaled {
		return NewParsedCheckOutcome(CheckOutcomeSignaled, nil)
	}
	if crashed {
		return NewParsedCheckOutcome(CheckOutcomeCrashed, nil)
	}
	if result.exitCode > 1 {
		return NewParsedCheckOutcome(CheckOutcomeInfrastructureFailed, nil)
	}
	if len(referencedBuilds) != 0 {
		return NewParsedCheckOutcome(CheckOutcomeCompilationFailed, nil)
	}
	setupFailure := false
	for packageName := range packageFailures {
		if _, hasNamedFailure := namedFailurePackages[packageName]; !hasNamedFailure {
			setupFailure = true
			break
		}
	}
	identities := make([]string, 0, len(failures))
	for identity := range failures {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	if result.exitCode == 0 {
		if len(packageFailures) != 0 || len(identities) != 0 {
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
		return NewParsedCheckOutcome(CheckOutcomePassed, nil)
	}
	if setupFailure {
		return NewParsedCheckOutcome(CheckOutcomeSetupFailed, nil)
	}
	if len(identities) != 0 {
		return NewParsedCheckOutcome(CheckOutcomeAssertionFailed, identities)
	}
	if len(packageFailures) != 0 {
		return NewParsedCheckOutcome(CheckOutcomeSetupFailed, nil)
	}
	return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
}

func goTestPackageAbnormalExit(output string) (signaled, crashed bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "signal:") && len(strings.TrimSpace(strings.TrimPrefix(line, "signal:"))) != 0 {
			signaled = true
			continue
		}
		const exitStatus = "exit status "
		if !strings.HasPrefix(line, exitStatus) {
			continue
		}
		code, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, exitStatus)))
		if err == nil && code != 0 {
			crashed = true
		}
	}
	return signaled, crashed
}

type structuredResultItem struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type structuredCheckDocument struct {
	SchemaVersion int                     `json:"schema_version"`
	Status        string                  `json:"status"`
	Assertions    *[]structuredResultItem `json:"assertions"`
	Diagnostics   *[]structuredResultItem `json:"diagnostics"`
}

func parseAssertionDocument(result CheckProcessResult, diagnostics bool) (ParsedCheckOutcome, error) {
	if err := rejectDuplicateJSONObjectKeys(result.stdout); err != nil {
		return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
	}
	var document structuredCheckDocument
	decoder := json.NewDecoder(bytes.NewReader(result.stdout))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
	}
	if document.SchemaVersion != 1 || (document.Status != "passed" && document.Status != "failed") {
		return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
	}
	var items *[]structuredResultItem
	if diagnostics {
		if document.Diagnostics == nil || document.Assertions != nil {
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
		items = document.Diagnostics
	} else {
		if document.Assertions == nil || document.Diagnostics != nil {
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
		items = document.Assertions
	}
	failures := make([]string, 0)
	seen := make(map[string]struct{}, len(*items))
	for _, item := range *items {
		if item.ID == "" || strings.TrimSpace(item.ID) != item.ID || len(item.ID) > maxCheckFailureIDBytes ||
			strings.ContainsAny(item.ID, "\x00\r\n") || (item.Status != "passed" && item.Status != "failed") {
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
		if _, exists := seen[item.ID]; exists {
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
		seen[item.ID] = struct{}{}
		if item.Status == "failed" {
			failures = append(failures, item.ID)
		}
	}
	sort.Strings(failures)
	if result.exitCode == 0 {
		if document.Status != "passed" || len(failures) != 0 {
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
		return NewParsedCheckOutcome(CheckOutcomePassed, nil)
	}
	if document.Status != "failed" || len(failures) == 0 {
		return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
	}
	if diagnostics {
		return NewParsedCheckOutcome(CheckOutcomeDiagnosticFailed, failures)
	}
	return NewParsedCheckOutcome(CheckOutcomeAssertionFailed, failures)
}

func rejectDuplicateJSONObjectKeys(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := consumeUniqueJSONValue(decoder, 0); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("structured JSON contains trailing data")
		}
		return err
	}
	return nil
}

func consumeUniqueJSONValue(decoder *json.Decoder, depth uint8) error {
	if depth > 64 {
		return fmt.Errorf("structured JSON exceeds its nesting bound")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("structured JSON object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("structured JSON contains duplicate key %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeUniqueJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("structured JSON object is incomplete")
		}
	case '[':
		for decoder.More() {
			if err := consumeUniqueJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("structured JSON array is incomplete")
		}
	default:
		return fmt.Errorf("structured JSON contains an invalid delimiter")
	}
	return nil
}
