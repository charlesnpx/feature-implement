package workspace

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// integrationCompletionReservationBytes returns a conservative upper bound for
// the one completion record required by the pending integration intent. New
// attempts are barred while an intent is pending, so the set of possible
// superseded attempts cannot grow. The mutable fields on those attempts are
// expanded to their largest valid encodings.
func integrationCompletionReservationBytes(
	runtime WorkspaceRuntimeProjection,
) (int64, error) {
	var winner *RuntimeAttemptProjection
	for index := range runtime.attempts {
		attempt := &runtime.attempts[index]
		if attempt.integration == nil ||
			attempt.integration.Integrated() {
			continue
		}
		if winner != nil {
			return 0, fmt.Errorf(
				"integration completion capacity cannot reserve for multiple pending intents",
			)
		}
		winner = attempt
	}
	if winner == nil {
		return 0, nil
	}
	if winner.phase != AttemptActive || winner.leaseID.IsZero() {
		return 0, fmt.Errorf(
			"pending integration completion has no active leased winner",
		)
	}

	superseded, err := integrationSupersededAttempts(
		runtime, winner.attemptID,
	)
	if err != nil {
		return 0, err
	}
	maximumPhase := longestIntegrationNonterminalPhase()
	for index := range superseded {
		superseded[index].phase = maximumPhase
		superseded[index].leaseID =
			maximumIntegrationReservationID(index, winner.leaseID)
		superseded[index].serialSegmentHeld =
			!superseded[index].serialSegment.IsZero()
	}
	serialSegment := ID{}
	if winner.serialSegmentHeld {
		serialSegment = winner.serialSegment
	}
	event, err := newMergeUnitIntegratedJournalEvent(
		winner.integration.intent,
		winner.leaseID,
		serialSegment,
		superseded,
	)
	if err != nil {
		return 0, err
	}
	return maximumEncodedIntegrationCompletionBytes(event)
}

func longestIntegrationNonterminalPhase() AttemptRuntimePhase {
	longest := AttemptRuntimePhase("")
	for _, phase := range []AttemptRuntimePhase{
		AttemptReserved,
		AttemptMaterializing,
		AttemptActive,
		AttemptPaused,
		AttemptReviewExhausted,
	} {
		if len(phase) > len(longest) {
			longest = phase
		}
	}
	return longest
}

func maximumIntegrationReservationID(index int, excluded ID) ID {
	suffix := strconv.Itoa(index + 1)
	padding := maxIdentifierBytes - 1 - len(suffix)
	if padding < 0 {
		padding = 0
	}
	value := "l" + strings.Repeat("0", padding) + suffix
	if len(value) > maxIdentifierBytes {
		value = value[len(value)-maxIdentifierBytes:]
		value = "l" + value[1:]
	}
	if value == excluded.String() {
		value = "m" + value[1:]
	}
	return MustID(value)
}

func maximumEncodedIntegrationCompletionBytes(
	event MergeUnitIntegratedJournalEvent,
) (int64, error) {
	record := JournalRecord{
		sequence: math.MaxUint64,
		occurredAt: time.Date(
			9999, time.December, 31, 23, 59, 59,
			999999999, time.UTC,
		),
		previousHash: DigestBytes(
			[]byte("maximum integration completion previous hash"),
		),
		generation: event.boundGeneration(),
		event:      cloneWorkspaceJournalEvent(event),
	}
	body, err := marshalJournalRecordBody(record)
	if err != nil {
		return 0, err
	}
	record.eventHash = DigestBytes(body)
	encoded, err := marshalJournalRecord(record)
	if err != nil {
		return 0, err
	}
	if len(encoded) == 0 || len(encoded) > MaxJournalRecordBytes {
		return 0, fmt.Errorf(
			"maximum integration completion record exceeds %d bytes",
			MaxJournalRecordBytes,
		)
	}
	return int64(len(encoded) + 1), nil
}

func requireIntegrationCompletionReservation(
	snapshot JournalSnapshot,
	runtime WorkspaceRuntimeProjection,
) error {
	reserved, err := integrationCompletionReservationBytes(runtime)
	if err != nil {
		return err
	}
	if reserved == 0 {
		return fmt.Errorf(
			"integration completion capacity requires one pending intent",
		)
	}
	return validateJournalAppendCapacity(
		snapshot.byteLength, 0, reserved,
	)
}
