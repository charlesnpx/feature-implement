package workspace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
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
		Test        string  `json:"Test"`
		Output      string  `json:"Output"`
		FailedBuild string  `json:"FailedBuild"`
		Elapsed     float64 `json:"Elapsed"`
		Time        string  `json:"Time"`
	}
	scanner := bufio.NewScanner(bytes.NewReader(result.stdout))
	scanner.Buffer(make([]byte, 64*1024), maxAttemptGitOutputBytes)
	failures := make(map[string]struct{})
	structured, terminal, packageFailure, buildFailure := 0, false, false, false
	crashed, timedOut := false, false
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event goTestEvent
		if err := json.Unmarshal(line, &event); err != nil || event.Action == "" {
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
		structured++
		switch event.Action {
		case "start", "run", "pause", "cont", "bench":
		case "output", "build-output":
			text := strings.ToLower(event.Output)
			if strings.Contains(text, "test timed out after") || strings.Contains(text, "panic: test timed out") {
				timedOut = true
			}
			if strings.Contains(text, "panic:") || strings.Contains(text, "fatal error:") ||
				strings.Contains(text, "signal: aborted") || strings.Contains(text, "signal: segmentation fault") {
				crashed = true
			}
			if strings.Contains(text, "[build failed]") || strings.Contains(text, "undefined:") ||
				strings.Contains(text, "build constraints exclude all go files") || strings.Contains(text, "cannot find package") {
				buildFailure = true
			}
		case "build-fail":
			buildFailure, terminal = true, true
		case "pass", "skip":
			if event.Test == "" {
				terminal = true
			}
		case "fail":
			if event.Test != "" {
				identity, err := GoTestFailureIdentity(event.Package, event.Test)
				if err != nil {
					return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
				}
				failures[identity] = struct{}{}
			} else {
				packageFailure, terminal = true, true
				if event.FailedBuild != "" {
					buildFailure = true
				}
			}
		default:
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
	}
	if err := scanner.Err(); err != nil || structured == 0 || !terminal {
		return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
	}
	if timedOut {
		return NewParsedCheckOutcome(CheckOutcomeTimedOut, nil)
	}
	if crashed {
		return NewParsedCheckOutcome(CheckOutcomeCrashed, nil)
	}
	if buildFailure {
		return NewParsedCheckOutcome(CheckOutcomeCompilationFailed, nil)
	}
	identities := make([]string, 0, len(failures))
	for identity := range failures {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	if result.exitCode == 0 {
		if packageFailure || len(identities) != 0 {
			return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
		}
		return NewParsedCheckOutcome(CheckOutcomePassed, nil)
	}
	if len(identities) != 0 {
		return NewParsedCheckOutcome(CheckOutcomeAssertionFailed, identities)
	}
	if packageFailure {
		return NewParsedCheckOutcome(CheckOutcomeSetupFailed, nil)
	}
	return NewParsedCheckOutcome(CheckOutcomeMalformedOutput, nil)
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
