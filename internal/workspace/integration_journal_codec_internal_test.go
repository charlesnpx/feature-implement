package workspace

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestIntegrationIntentIsDeterministicForSupportedObjectFormats(t *testing.T) {
	for _, algorithm := range []GitHashAlgorithm{GitHashSHA1, GitHashSHA256} {
		t.Run(string(algorithm), func(t *testing.T) {
			options := integrationIntentTestOptions(t, algorithm)
			options.OccurredAt = time.Date(
				2026, time.July, 25, 12, 34, 56, 987654321, time.UTC,
			)
			first, err := NewMergeUnitIntegrationIntent(options)
			if err != nil {
				t.Fatal(err)
			}
			options.OccurredAt = options.OccurredAt.Truncate(time.Second)
			second, err := NewMergeUnitIntegrationIntent(options)
			if err != nil {
				t.Fatal(err)
			}
			if first.Digest() != second.Digest() ||
				first.ExpectedMerge() != second.ExpectedMerge() ||
				string(first.CommitContent()) != string(second.CommitContent()) {
				t.Fatalf(
					"whole-second deterministic intent mismatch:\nfirst=%#v\nsecond=%#v",
					first, second,
				)
			}
			parents := first.Parents()
			if len(parents) != 2 ||
				parents[0] != options.ExpectedFeatureHead ||
				parents[1] != options.AcceptedHead {
				t.Fatalf("integration parents = %#v", parents)
			}
			for _, fragment := range []string{
				"Plan: alpha-plan",
				"Merge-Unit: unit-one",
				"Attempt: attempt-one",
				"Generation: " + options.Generation.String(),
				"Accepted-Head: " + options.AcceptedHead.String(),
				"Acceptance: adopted-head:" +
					options.AdoptedHeadEventDigest.String(),
			} {
				if !strings.Contains(first.Message(), fragment) {
					t.Fatalf(
						"integration message missing %q:\n%s",
						fragment, first.Message(),
					)
				}
			}
			if first.AuthorAt().Location() != time.UTC ||
				first.AuthorAt().Nanosecond() != 0 ||
				first.CommitterAt() != first.AuthorAt() {
				t.Fatalf(
					"integration timestamps are not canonical: author=%s committer=%s",
					first.AuthorAt(), first.CommitterAt(),
				)
			}
			recomputed, err := gitCommitObjectID(
				algorithm, first.CommitContent(),
			)
			if err != nil {
				t.Fatal(err)
			}
			if recomputed != first.ExpectedMerge() {
				t.Fatalf(
					"expected merge = %s, recomputed %s",
					first.ExpectedMerge(), recomputed,
				)
			}
		})
	}
}

func TestIntegrationJournalCodecRoundTripsAndRejectsTampering(t *testing.T) {
	intent, err := NewMergeUnitIntegrationIntent(
		integrationIntentTestOptions(t, GitHashSHA1),
	)
	if err != nil {
		t.Fatal(err)
	}
	intended, err := NewMergeUnitIntegrationIntendedJournalEvent(intent)
	if err != nil {
		t.Fatal(err)
	}
	payload, supported, err := marshalIntegrationJournalEvent(intended)
	if err != nil || !supported {
		t.Fatalf("marshal integration intent: supported=%t err=%v", supported, err)
	}
	decoded, supported, err := decodeIntegrationJournalEvent(
		JournalEventMergeUnitIntegrationIntended, payload,
	)
	if err != nil || !supported {
		t.Fatalf("decode integration intent: supported=%t err=%v", supported, err)
	}
	replayed, ok := decoded.(MergeUnitIntegrationIntendedJournalEvent)
	if !ok || replayed.intent.digest != intent.digest ||
		replayed.intent.expectedMerge != intent.expectedMerge {
		t.Fatalf("replayed integration intent = %#v", decoded)
	}

	completed, err := NewMergeUnitIntegratedJournalEvent(intent)
	if err != nil {
		t.Fatal(err)
	}
	payload, supported, err = marshalIntegrationJournalEvent(completed)
	if err != nil || !supported {
		t.Fatalf(
			"marshal integration completion: supported=%t err=%v",
			supported, err,
		)
	}
	decoded, supported, err = decodeIntegrationJournalEvent(
		JournalEventMergeUnitIntegrated, payload,
	)
	if err != nil || !supported {
		t.Fatalf(
			"decode integration completion: supported=%t err=%v",
			supported, err,
		)
	}
	replayedCompletion, ok := decoded.(MergeUnitIntegratedJournalEvent)
	if !ok || replayedCompletion.intentDigest != intent.digest ||
		replayedCompletion.mergeCommit != intent.expectedMerge {
		t.Fatalf("replayed integration completion = %#v", decoded)
	}

	wire := integrationIntentDigestValue(intent)
	wire.Message += "tampered\n"
	tampered, err := json.Marshal(integrationIntendedPayloadWire{
		Intent:       wire,
		IntentDigest: intent.digest.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeIntegrationJournalEvent(
		JournalEventMergeUnitIntegrationIntended, tampered,
	); err == nil {
		t.Fatal("tampered integration message was accepted")
	}

	wire = integrationIntentDigestValue(intent)
	wire.Parents[0], wire.Parents[1] = wire.Parents[1], wire.Parents[0]
	tampered, err = json.Marshal(integrationIntendedPayloadWire{
		Intent:       wire,
		IntentDigest: intent.digest.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeIntegrationJournalEvent(
		JournalEventMergeUnitIntegrationIntended, tampered,
	); err == nil {
		t.Fatal("tampered integration parent order was accepted")
	}

	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatal(err)
	}
	object["unexpected"] = true
	tampered, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeIntegrationJournalEvent(
		JournalEventMergeUnitIntegrated, tampered,
	); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("completion with unknown field error = %v", err)
	}
}

func TestIntegrationIntentRequiresExactlyOneAcceptanceMode(t *testing.T) {
	options := integrationIntentTestOptions(t, GitHashSHA1)
	options.ReviewReadinessDigest =
		DigestBytes([]byte("review-readiness"))
	if _, err := NewMergeUnitIntegrationIntent(options); err == nil ||
		!strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("dual acceptance evidence error = %v", err)
	}
	options.AdoptedHeadEventDigest = Digest{}
	intent, err := NewMergeUnitIntegrationIntent(options)
	if err != nil {
		t.Fatal(err)
	}
	if intent.AcceptanceMode() != IntegrationAcceptanceReviewReady ||
		intent.ReviewReadinessDigest() != options.ReviewReadinessDigest ||
		!intent.AdoptedHeadEventDigest().IsZero() {
		t.Fatalf("review-ready intent = %#v", intent)
	}
	options.ReviewReadinessDigest = Digest{}
	if _, err := NewMergeUnitIntegrationIntent(options); err == nil ||
		!strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("missing acceptance evidence error = %v", err)
	}
}

func TestIntegrationCommitTreeInspectionDoesNotImposeMessagePolicy(
	t *testing.T,
) {
	tree := integrationTestObject(t, GitHashSHA1, 'a')
	content := []byte(
		"tree " + gitObjectHex(tree) + "\n" +
			"author External User <external@example.invalid> 1 +0000\n" +
			"committer External User <external@example.invalid> 1 +0000\n" +
			"\n" +
			"subject\nbody without tool-specific blank framing\n",
	)
	parsed, err := parseIntegrationCommitTree(content, GitHashSHA1)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != tree {
		t.Fatalf("parsed integration tree = %s, want %s", parsed, tree)
	}
	if _, _, _, _, err := parseRawCommitObject(
		content, GitHashSHA1,
	); err == nil {
		t.Fatal(
			"test fixture unexpectedly satisfies the tool commit-message policy",
		)
	}
}

func integrationIntentTestOptions(
	t *testing.T,
	algorithm GitHashAlgorithm,
) MergeUnitIntegrationIntentOptions {
	t.Helper()
	mergeUnit, err := NewMergeUnitReference(
		MustID("alpha-plan"), MustID("unit-one"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return MergeUnitIntegrationIntentOptions{
		WorkspaceID:         MustID("example-workspace"),
		Generation:          DigestBytes([]byte("integration-generation")),
		AttemptID:           MustID("attempt-one"),
		MergeUnit:           mergeUnit,
		FeatureRef:          "refs/heads/feature/example-workspace",
		ExpectedFeatureHead: integrationTestObject(t, algorithm, 'a'),
		AcceptedHead:        integrationTestObject(t, algorithm, 'b'),
		AcceptedTree:        integrationTestObject(t, algorithm, 'c'),
		AdoptedHeadEventDigest: DigestBytes(
			[]byte("adopted-head-event"),
		),
		OccurredAt: time.Date(
			2026, time.July, 25, 12, 34, 56, 0, time.UTC,
		),
	}
}

func integrationTestObject(
	t *testing.T,
	algorithm GitHashAlgorithm,
	digit byte,
) GitObjectID {
	t.Helper()
	size := 40
	if algorithm == GitHashSHA256 {
		size = 64
	}
	object, err := ParseGitObjectID(
		string(algorithm) + ":" + strings.Repeat(string(digit), size),
	)
	if err != nil {
		t.Fatal(err)
	}
	return object
}
