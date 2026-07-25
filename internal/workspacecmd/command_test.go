package workspacecmd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/charlesnpx/feature-implement/internal/workspace"
)

func TestLocalCommandDecodersRequireExactReceiptFreeFields(t *testing.T) {
	tests := []struct {
		name   string
		source string
		target func() any
		want   string
	}{
		{
			name: "init requires worktree root",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z"
}`,
			target: func() any { return &initializeRequest{} },
			want:   "worktree_root",
		},
		{
			name: "reserve rejects caller base",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "plan_id": "plan-one",
  "merge_unit_id": "unit-one",
  "attempt_number": 1,
  "goal": {"id": "goal-one", "scope": "merge_unit"},
  "base": "sha1:1111111111111111111111111111111111111111"
}`,
			target: func() any { return &reserveAttemptInput{} },
			want:   "unknown field",
		},
		{
			name: "acknowledgement requires directive",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one",
  "kind": "goal_completed",
  "goal": {"id": "goal-one", "scope": "merge_unit"},
  "idempotency_key": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}`,
			target: func() any { return &acknowledgeInput{} },
			want:   "directive_digest",
		},
		{
			name: "acknowledgement rejects receipt",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one",
  "kind": "goal_completed",
  "directive_digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "goal": {"id": "goal-one", "scope": "merge_unit"},
  "idempotency_key": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "receipt": {}
}`,
			target: func() any { return &acknowledgeInput{} },
			want:   "unknown field",
		},
		{
			name: "owner response requires exact head",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one",
  "boundary_id": "boundary-one",
  "directive_digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "goal": {"id": "goal-one", "scope": "merge_unit"},
  "response": "continue"
}`,
			target: func() any { return &ownerResponseInput{} },
			want:   "expected_head",
		},
		{
			name: "review result rejects receipt",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one",
  "reservation_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "request_digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "reviewer_instance": "reviewer-one",
  "status": "completed",
  "findings": [],
  "isolation": {
    "repository_read_only": true,
    "scratch_ephemeral": true,
    "repository_hooks": false,
    "write_network": false,
    "external_write": false
  },
  "receipt": {}
}`,
			target: func() any { return &recordReviewInput{} },
			want:   "unknown field",
		},
		{
			name: "non-applying review fix rejects body",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one",
  "ordinal": 1,
  "accepted_finding_ids": [],
  "body": "ignored"
}`,
			target: func() any { return &reviewFixInput{} },
			want:   "unknown field",
		},
		{
			name: "integration requires attempt",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z"
}`,
			target: func() any { return &integrateMergeUnitInput{} },
			want:   "attempt_id",
		},
		{
			name: "completion rejects attempt",
			source: `{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one"
}`,
			target: func() any { return &completeVerifyInput{} },
			want:   "unknown field",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := decodeRequest([]byte(test.source), test.target())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decode error = %v, want %q", err, test.want)
			}
		})
	}

	var completion completeVerifyInput
	if err := decodeRequest([]byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z"
}`), &completion); err != nil {
		t.Fatalf("valid completion envelope: %v", err)
	}
}

func TestRequestSchemasExposeOnlySupportedLocalMutations(t *testing.T) {
	encoded, err := json.Marshal(RequestSchemas())
	if err != nil {
		t.Fatal(err)
	}
	source := string(encoded)
	for _, forbidden := range []string{
		`"receipt"`, `"reconcile.`, `"control.`, `"provider.`,
		`"provider_broker"`, `"commit.rebase"`, `"base"`,
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf(
				"request schemas expose removed field %q: %s",
				forbidden, source,
			)
		}
	}
	schemas := RequestSchemas()["requests"].(map[string]any)
	for _, required := range []string{
		"init",
		"recover",
		"attempt.reserve",
		"attempt.materialize",
		"attempt.adopt-head",
		"attempt.boundary",
		"attempt.next-goal",
		"attempt.acknowledge",
		"attempt.owner-response",
		"attempt.resume",
		"commit.next",
		"review.start",
		"review.reserve",
		"review.record",
		"review.reserve-fix",
		"review.apply-fix",
		"review.record-fix",
		"review.ready",
		"integrate.merge-unit",
		"complete.verify",
	} {
		if _, exists := schemas[required]; !exists {
			t.Fatalf("request schemas omit %s", required)
		}
	}
	if len(schemas) != 20 {
		t.Fatalf("request schema count = %d: %+v", len(schemas), schemas)
	}
	for _, action := range []string{
		"review.reserve-fix",
		"review.record-fix",
	} {
		properties := schemas[action].(map[string]any)["properties"].(map[string]any)
		if _, exists := properties["body"]; exists {
			t.Fatalf("%s schema accepts ignored body: %+v", action, properties)
		}
	}
	applyProperties := schemas["review.apply-fix"].(map[string]any)["properties"].(map[string]any)
	if _, exists := applyProperties["body"]; !exists {
		t.Fatalf("review.apply-fix schema omits body: %+v", applyProperties)
	}
}

func TestDecodeRequestKeepsSchemaOptionalFieldsOptional(t *testing.T) {
	var input commitNextInput
	if err := decodeRequest([]byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one"
}`), &input); err != nil {
		t.Fatalf("optional commit body was required: %v", err)
	}
	var reviewFix applyReviewFixInput
	if err := decodeRequest([]byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one",
  "ordinal": 1,
  "accepted_finding_ids": [],
  "body": "apply the accepted fixes"
}`), &reviewFix); err != nil {
		t.Fatalf("valid review fix body: %v", err)
	}
}

func TestReportDirectiveSchemaKeepsChoicesOptional(t *testing.T) {
	reports := ReportSchemas()["reports"].(map[string]any)
	scheduler := reports["scheduler"].(map[string]any)
	properties := scheduler["properties"].(map[string]any)
	units := properties["units"].(map[string]any)
	unit := units["items"].(map[string]any)
	unitProperties := unit["properties"].(map[string]any)
	directives := unitProperties["pending_directives"].(map[string]any)
	directive := directives["items"].(map[string]any)
	required := directive["required"].([]string)
	for _, name := range required {
		if name == "choices" {
			t.Fatalf("directive schema still requires omitted empty choices: %+v", required)
		}
	}
	directiveProperties := directive["properties"].(map[string]any)
	if _, exists := directiveProperties["choices"]; !exists {
		t.Fatalf("directive schema no longer describes choices: %+v", directiveProperties)
	}
}

func TestDeferredLocalCommandsStrictlyDecodeTheirFinalEnvelopes(
	t *testing.T,
) {
	_, err := executeIntegration(
		context.Background(),
		workspace.WorkspaceBundle{},
		Options{
			Subaction: "merge-unit",
			Input: []byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "attempt_id": "attempt-one"
}`),
		},
	)
	if err == nil ||
		!strings.Contains(err.Error(), "workspace directory is required") {
		t.Fatalf("valid integration envelope error = %v", err)
	}
	_, err = executeCompletion(
		workspace.WorkspaceBundle{},
		Options{
			Subaction: "verify",
			Input: []byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z"
}`),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("valid completion envelope error = %v", err)
	}
}
