package workspace

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
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

	supersededMergeUnit, err := NewMergeUnitReference(
		MustID("alpha-plan"), MustID("unit-two"),
	)
	if err != nil {
		t.Fatal(err)
	}
	superseded := []integrationSupersededAttempt{
		{
			attemptID:         MustID("attempt-two"),
			mergeUnit:         supersededMergeUnit,
			base:              intent.expectedFeatureHead,
			phase:             AttemptActive,
			leaseID:           MustID("lease-two"),
			serialSegment:     MustID("serial-two"),
			serialSegmentHeld: true,
		},
	}
	completed, err := newMergeUnitIntegratedJournalEvent(
		intent, MustID("lease-one"), MustID("serial-one"),
		superseded,
	)
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
		replayedCompletion.mergeCommit != intent.expectedMerge ||
		!equalIntegrationSupersededAttempts(
			replayedCompletion.supersededAttempts, superseded,
		) {
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
	for _, test := range []struct {
		name   string
		mutate func(*integrationIntentDigestWire)
	}{
		{
			name: "expected absent feature ref",
			mutate: func(wire *integrationIntentDigestWire) {
				wire.ExpectedFeatureRefAbsent = !wire.ExpectedFeatureRefAbsent
			},
		},
		{
			name: "attempt Git directory",
			mutate: func(wire *integrationIntentDigestWire) {
				wire.AttemptWorktreeBinding.GitDirectory += "-replacement"
			},
		},
	} {
		wire = integrationIntentDigestValue(intent)
		test.mutate(&wire)
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
			t.Fatalf(
				"tampered integration %s was accepted",
				test.name,
			)
		}
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

	var completedWire integrationCompletedPayloadWire
	if err := json.Unmarshal(payload, &completedWire); err != nil {
		t.Fatal(err)
	}
	completedWire.SupersededAttempts[0].Phase = AttemptCompleted
	tampered, err = json.Marshal(completedWire)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeIntegrationJournalEvent(
		JournalEventMergeUnitIntegrated, tampered,
	); err == nil ||
		!strings.Contains(err.Error(), "superseded attempt") {
		t.Fatalf("completion with terminal superseded attempt error = %v", err)
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

func TestIntegrationCompletionReducerRequiresExactLeaseAndSerialSegment(
	t *testing.T,
) {
	intent, err := NewMergeUnitIntegrationIntent(
		integrationIntentTestOptions(t, GitHashSHA1),
	)
	if err != nil {
		t.Fatal(err)
	}
	targetRoot := t.TempDir()
	targetBinding, err := NewLocalTargetBinding(
		LocalTargetBindingOptions{
			Root:          targetRoot,
			ObjectFormat:  GitHashSHA1,
			BaseRef:       "refs/heads/main",
			BaseCommit:    intent.expectedFeatureHead,
			FeatureBranch: "feature/example-workspace",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	leaseID := MustID("lease-one")
	serialSegment := MustID("serial-one")
	current := WorkspaceRuntimeProjection{
		workspaceID:      intent.workspaceID,
		activeGeneration: intent.generation,
		localTarget: RuntimeLocalTargetProjection{
			binding: targetBinding, createdHead: intent.expectedFeatureHead,
			createdRecord: 2, headRecord: 2,
		},
		attempts: []RuntimeAttemptProjection{
			{
				attemptID:         intent.attemptID,
				mergeUnit:         intent.mergeUnit,
				generation:        intent.generation,
				base:              intent.expectedFeatureHead,
				phase:             AttemptActive,
				verifiedHead:      intent.acceptedHead,
				leaseID:           leaseID,
				serialSegment:     serialSegment,
				serialSegmentHeld: true,
				integration: &RuntimeIntegrationProjection{
					intent: intent, intentRecord: 3,
				},
			},
		},
	}
	for _, test := range []struct {
		name    string
		lease   ID
		segment ID
	}{
		{
			name:  "wrong lease",
			lease: MustID("lease-other"), segment: serialSegment,
		},
		{
			name:  "missing segment",
			lease: leaseID,
		},
		{
			name:  "wrong segment",
			lease: leaseID, segment: MustID("serial-other"),
		},
	} {
		event, err := NewMergeUnitIntegratedJournalEvent(
			intent, test.lease, test.segment,
		)
		if err != nil {
			t.Fatal(err)
		}
		next := cloneWorkspaceRuntime(current)
		err = reduceIntegrationRuntime(
			current, &next, JournalRecord{
				sequence: 4, generation: intent.generation,
				event: event,
			},
		)
		if err == nil ||
			!strings.Contains(err.Error(), "lease") {
			t.Fatalf("%s completion reducer error = %v", test.name, err)
		}
	}
	event, err := NewMergeUnitIntegratedJournalEvent(
		intent, leaseID, serialSegment,
	)
	if err != nil {
		t.Fatal(err)
	}
	next := cloneWorkspaceRuntime(current)
	if err := reduceIntegrationRuntime(
		current, &next, JournalRecord{
			sequence: 4, generation: intent.generation,
			event: event,
		},
	); err != nil {
		t.Fatal(err)
	}
	if next.attempts[0].phase != AttemptCompleted ||
		!next.attempts[0].leaseID.IsZero() ||
		next.attempts[0].serialSegmentHeld {
		t.Fatalf("exact completion reducer result = %#v", next.attempts[0])
	}

	loserMergeUnit, err := NewMergeUnitReference(
		MustID("alpha-plan"), MustID("unit-two"),
	)
	if err != nil {
		t.Fatal(err)
	}
	loserLease := MustID("loser-lease")
	loserSegment := MustID("loser-segment")
	withLoser := cloneWorkspaceRuntime(current)
	withLoser.attempts = append(
		withLoser.attempts,
		RuntimeAttemptProjection{
			attemptID:         MustID("attempt-two"),
			mergeUnit:         loserMergeUnit,
			generation:        intent.generation,
			base:              intent.expectedFeatureHead,
			phase:             AttemptActive,
			leaseID:           loserLease,
			serialSegment:     loserSegment,
			serialSegmentHeld: true,
		},
	)
	missingSuperseded, err := NewMergeUnitIntegratedJournalEvent(
		intent, leaseID, serialSegment,
	)
	if err != nil {
		t.Fatal(err)
	}
	next = cloneWorkspaceRuntime(withLoser)
	if err := reduceIntegrationRuntime(
		withLoser, &next, JournalRecord{
			sequence: 4, generation: intent.generation,
			event: missingSuperseded,
		},
	); err == nil ||
		!strings.Contains(err.Error(), "exact superseded attempts") {
		t.Fatalf("completion missing superseded attempt error = %v", err)
	}
	supersededAttempts, err := integrationSupersededAttempts(
		withLoser, intent.attemptID,
	)
	if err != nil {
		t.Fatal(err)
	}
	exactWithLoser, err := newMergeUnitIntegratedJournalEvent(
		intent, leaseID, serialSegment, supersededAttempts,
	)
	if err != nil {
		t.Fatal(err)
	}
	next = cloneWorkspaceRuntime(withLoser)
	if err := reduceIntegrationRuntime(
		withLoser, &next, JournalRecord{
			sequence: 4, generation: intent.generation,
			event: exactWithLoser,
		},
	); err != nil {
		t.Fatal(err)
	}
	if next.attempts[1].phase != AttemptSuperseded ||
		!next.attempts[1].leaseID.IsZero() ||
		next.attempts[1].serialSegmentHeld {
		t.Fatalf(
			"superseded completion reducer result = %#v",
			next.attempts[1],
		)
	}
}

func TestIntegrationCompletionCapacityRejectsOversizedSupersededSetAndReservesJournalTail(
	t *testing.T,
) {
	intent, err := NewMergeUnitIntegrationIntent(
		integrationIntentTestOptions(t, GitHashSHA256),
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime := WorkspaceRuntimeProjection{
		attempts: []RuntimeAttemptProjection{
			{
				attemptID:  intent.attemptID,
				mergeUnit:  intent.mergeUnit,
				generation: intent.generation,
				base:       intent.expectedFeatureHead,
				phase:      AttemptActive,
				leaseID:    MustID("winner-lease"),
				integration: &RuntimeIntegrationProjection{
					intent: intent, intentRecord: 1,
				},
			},
		},
	}
	for index := 0; index < 8; index++ {
		unit, unitErr := NewMergeUnitReference(
			MustID("alpha-plan"),
			MustID(fmt.Sprintf("unit-%04d", index)),
		)
		if unitErr != nil {
			t.Fatal(unitErr)
		}
		runtime.attempts = append(
			runtime.attempts,
			RuntimeAttemptProjection{
				attemptID: MustID(
					fmt.Sprintf("attempt-%04d", index),
				),
				mergeUnit:  unit,
				generation: intent.generation,
				base:       intent.expectedFeatureHead,
				phase:      AttemptActive,
			},
		)
	}
	reserved, err := integrationCompletionReservationBytes(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if reserved <= 0 || reserved > MaxJournalRecordBytes+1 {
		t.Fatalf(
			"integration completion reservation = %d", reserved,
		)
	}
	if err := validateJournalAppendCapacity(
		MaxJournalBytes-reserved-256,
		256,
		reserved,
	); err != nil {
		t.Fatalf("exact reserved journal tail was rejected: %v", err)
	}
	if err := validateJournalAppendCapacity(
		MaxJournalBytes-reserved-256,
		257,
		reserved,
	); err == nil ||
		!strings.Contains(err.Error(), "reserved") {
		t.Fatalf(
			"competing append consumed integration reservation: %v",
			err,
		)
	}
	if err := validateJournalAppendCapacity(
		MaxJournalBytes-reserved,
		reserved,
		0,
	); err != nil {
		t.Fatalf(
			"integration completion could not consume its released reservation: %v",
			err,
		)
	}
	if err := validateJournalAppendCapacity(
		math.MaxInt64, 1, 0,
	); err == nil {
		t.Fatal("overflowing journal capacity accounting was accepted")
	}
	maximumWinnerLease := maximumIntegrationReservationID(
		0, ID{},
	)
	if candidate := maximumIntegrationReservationID(
		0, maximumWinnerLease,
	); candidate == maximumWinnerLease ||
		len(candidate.String()) != maxIdentifierBytes {
		t.Fatalf(
			"maximum synthetic loser lease %q collides with winner %q",
			candidate, maximumWinnerLease,
		)
	}
	if err := requireIntegrationCompletionReservation(
		JournalSnapshot{
			byteLength: MaxJournalBytes - reserved + 1,
		},
		runtime,
	); err == nil ||
		!strings.Contains(err.Error(), "reserved") {
		t.Fatalf(
			"near-full journal accepted an impossible integration publication: %v",
			err,
		)
	}

	oversized := cloneWorkspaceRuntime(runtime)
	for index := len(oversized.attempts); index < 6000; index++ {
		unit, unitErr := NewMergeUnitReference(
			MustID("alpha-plan"),
			MustID(fmt.Sprintf("oversized-unit-%04d", index)),
		)
		if unitErr != nil {
			t.Fatal(unitErr)
		}
		oversized.attempts = append(
			oversized.attempts,
			RuntimeAttemptProjection{
				attemptID: MustID(
					fmt.Sprintf("oversized-attempt-%04d", index),
				),
				mergeUnit:  unit,
				generation: intent.generation,
				base:       intent.expectedFeatureHead,
				phase:      AttemptPaused,
			},
		)
	}
	if _, err := integrationCompletionReservationBytes(
		oversized,
	); err == nil ||
		!strings.Contains(
			err.Error(), "maximum integration completion record exceeds",
		) {
		t.Fatalf(
			"oversized superseded set capacity error = %v", err,
		)
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
	bindingRoot := t.TempDir()
	attemptBinding, err := NewAttemptWorktreeGitBinding(
		AttemptWorktreeGitBindingOptions{
			Worktree:        filepath.Join(bindingRoot, "attempt"),
			GitDirectory:    filepath.Join(bindingRoot, "git", "worktrees", "attempt"),
			CommonDirectory: filepath.Join(bindingRoot, "git"),
			AdministrationDigest: DigestBytes(
				[]byte("attempt-administration"),
			),
			ConfigurationDigest: DigestBytes(
				[]byte("attempt-configuration"),
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return MergeUnitIntegrationIntentOptions{
		WorkspaceID:            MustID("example-workspace"),
		Generation:             DigestBytes([]byte("integration-generation")),
		AttemptID:              MustID("attempt-one"),
		MergeUnit:              mergeUnit,
		FeatureRef:             "refs/heads/feature/example-workspace",
		ExpectedFeatureHead:    integrationTestObject(t, algorithm, 'a'),
		AttemptWorktreeBinding: attemptBinding,
		AcceptedHead:           integrationTestObject(t, algorithm, 'b'),
		AcceptedTree:           integrationTestObject(t, algorithm, 'c'),
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
