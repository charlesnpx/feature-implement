package workspacecmd

import (
	"strings"
	"testing"
)

func TestDecodeRequestEnforcesRequiredBooleanPresence(t *testing.T) {
	missing := []byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "reconciliation_pending": false,
  "drift_detected": false,
  "ambiguous_effect": false,
  "receipt": {}
}`)
	var input controlSafetyInput
	if err := decodeRequest(missing, &input); err == nil || !strings.Contains(err.Error(), "gates_blocked") {
		t.Fatalf("omitted required boolean error = %v", err)
	}

	present := []byte(`{
  "schema_version": 2,
  "occurred_at": "2026-07-22T10:00:00Z",
  "gates_blocked": false,
  "reconciliation_pending": false,
  "drift_detected": false,
  "ambiguous_effect": false,
  "receipt": {}
}`)
	if err := decodeRequest(present, &input); err != nil {
		t.Fatalf("present false booleans were rejected: %v", err)
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
}
