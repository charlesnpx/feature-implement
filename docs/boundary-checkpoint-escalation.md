# Boundary Checkpoints and Escalation

A proposal to split `AttemptBoundaryMode` into two fields, so that blocks of merge units can run to completion without stopping, while planned owner checkpoints and agent-raised exception stops each become explicit and enforceable.

## The problem

`boundary.mode` currently carries two unrelated jobs in one enum.

**Planned checkpoint.** The owner, ahead of time, says "after this unit, stop and tell me." Unconditional by design — nothing is wrong, the owner just wants a decision point there.

**Exception stop.** The agent, at runtime, says "this went sideways" or "a decision came up that isn't mine to make." Unschedulable by definition — if it could be predicted, it would be a checkpoint.

Config can only express the first. That is why a value like `no_pause_unless_problem` cannot work: "problem" is a judgment made hours after the config was read, and no enum value can evaluate it.

Two consequences follow from the conflation:

- Every merge unit must declare a mode (`newAttemptBoundaryPolicy` rejects a nil block), so a unit that never intends to pause still writes `mode: pause_only` — which reads as a promise to pause. The config cannot say "this block runs unattended."
- Whether an attempt actually stops is decided entirely by whether the runner calls `attempt boundary`. Today that is governed by a line of prose in `SKILL.md`, not by the runtime.

## Proposed schema

```yaml
merge_units:
  - plan_id: sample-plan
    merge_unit_id: first-contract
    profile: standard
    boundary:
      checkpoint: none            # none | pause_only | complete_goal_and_wait
      escalation: allowed         # allowed | forbidden
      serial_segment: serial-first-contract
```

`checkpoint` is the owner's planned gate. `escalation` is the agent's permission to stop on its own.

### Semantics

| checkpoint | escalation | Behavior |
|---|---|---|
| `none` | `allowed` | Runs unit to unit without stopping. The agent may still stop if it hits something real. **This is the block-of-units default.** |
| `none` | `forbidden` | Cannot stop for any reason. The agent must finish or fail. For unattended and CI runs. |
| `pause_only` | `allowed` | Stops at this unit, owner responds `continue`, resumes on the same goal. |
| `complete_goal_and_wait` | `allowed` | Goal is finished here; owner responds, a next goal is reserved and acked, attempt resumes on the new goal. |

`checkpoint: none` is the value that does not exist today, and it is the one that makes an unattended block expressible.

## Telling a checkpoint from an escalation

Both arrive through `attempt boundary`, so the request must say which it is. Add a kind to `RecordAttemptBoundaryRequest`:

- **`checkpoint`** — legal only when `checkpoint != none`. Takes the shape of the configured checkpoint value.
- **`escalation`** — legal only when `escalation: allowed`. Always takes the `pause_only` shape: an exception stop resumes the same goal, because it is not a goal handoff.

The kind is carried on `AttemptBoundaryReachedJournalEvent` and folded into the directive digest, so the journal records not just that an attempt stopped but whether the owner asked for the stop or the agent raised it. That distinction is currently unrecoverable from the journal.

## Enforcement points

| Location | Change |
|---|---|
| `internal/workspace/wire.go:102` | `attemptBoundaryPolicyWire` gains `Checkpoint` and `Escalation`, drops `Mode`. Decoding is strict (`decodeStrictV2`), so this is a breaking schema change. |
| `internal/workspace/policy.go` | Replace `AttemptBoundaryMode` with `AttemptCheckpointMode` + `AttemptEscalationPolicy`. `AttemptBoundaryPolicy` carries both plus `serialSegment`. |
| `internal/workspace/policy.go` | Add `AttemptBoundaryPolicy.validateStrengthens` — currently absent, unlike `ExecutionPolicy`. |
| `internal/workspace/attempt_lifecycle.go:156` | Reserve binds both fields into `AttemptReservedJournalEvent` alongside `serialSegment`. |
| `internal/workspace/attempt_lifecycle.go` (`RecordAttemptBoundary`) | Reject a `checkpoint` kind when `checkpoint: none`; reject an `escalation` kind when `escalation: forbidden`. Both before any journal append. |
| `internal/workspace/attempt_lifecycle.go` (`boundaryResult`) | Directive selection keys off the resolved shape rather than the raw mode. |
| `internal/workspace/attempt_projection.go` | `AttemptBoundaryReached` validates the kind against the reserved policy. Resume rules are unchanged: `pause_only` shape resumes the same goal, `complete_goal_and_wait` requires the acked next goal. |
| `internal/workspace/attempt_journal_events.go:102` | Reserve-event validation checks both fields. |
| `internal/workspace/attempt_journal_events.go` (`deriveBoundaryDirectiveDigest`) | Digest input includes the kind. Idempotency key stays keyed to the goal-handoff shape. |
| `internal/workspace/attempt_journal_codec.go:131,229` | Serialize and reconstruct both fields. |
| `internal/workspace/runtime_views.go` (`attemptBoundaryStatus`) | Report the kind so `status` and `report` show whether a stop was planned or raised. |

## Narrowing rules

The two fields have opposite polarity, and the check must respect that.

**`escalation` is an agent permission.** Narrowing removes it: `allowed → forbidden` is legal, `forbidden → allowed` is not. This matches `ExecutionPolicy`'s existing "permissions may only become false."

**`checkpoint` is an owner gate.** Narrowing *adds* one. A unit may go `none → pause_only` or `none → complete_goal_and_wait`, but may never drop a checkpoint its profile declared — that would let a unit delete an owner touchpoint the profile asked for.

`pause_only` and `complete_goal_and_wait` do not order against each other. One keeps the goal, the other requires a new one; neither contains the other. So the relation is a partial order, not a line, and a unit may not swap between them.

## Migration

Runtime state is already non-migratable — `feature.runtime.v4.json` is rejected outright if the marker does not match, and the README states earlier draft state is intentionally not interpreted. So the runtime side needs no migration path: regenerate.

For config, recommend the hard rename and bump `WorkspaceBundleSchemaVersion` to 3. Existing files translate mechanically:

```
mode: pause_only              →  checkpoint: pause_only
                                 escalation: allowed
mode: complete_goal_and_wait  →  checkpoint: complete_goal_and_wait
                                 escalation: allowed
```

That translation is behavior-preserving. Adopting `checkpoint: none` on units that should run unattended is a separate, deliberate edit.

Keeping `mode` as a decode-time alias is possible but not recommended — it preserves exactly the ambiguity this change exists to remove.

## Two fixes needed regardless

These are independent of the schema change and should land either way.

**The playbook forces a stop on every unit.** Step 3 of both `skills/claude/feature__colon__implement/SKILL.md` and the codex copy, and step 8 of the README, instruct the runner to submit `attempt boundary` unconditionally. Nothing in the runtime requires it. This alone is why blocks of units do not run through today.

**That step, as written, cannot succeed.** Both documents order it *after* `integrate merge-unit`. But integration sets the attempt to `AttemptCompleted` and clears the lease:

```go
updated.phase = AttemptCompleted
updated.serialSegmentHeld = false
updated.leaseID = ID{}
```

while `RecordAttemptBoundary` requires `AttemptActive`. So the documented step returns `attempt %s must be active to reach a boundary`. Any boundary must be recorded before integration, and the docs should say so.

## Optional cleanup

`serial_segment` lives under `boundary` but is consumed at reserve time for scheduling, whether or not a pause ever happens. It is not boundary policy. Since the schema is being broken anyway, this is the cheapest moment to lift it to the merge-unit level.

## Why not a single richer enum

An `escalation` value folded into `checkpoint` would produce combinations that do not mean anything — a planned goal-handoff checkpoint that also forbids exception stops is coherent, but expressing it needs both axes anyway. Two fields with two and three values beat one field with six, and they keep the narrowing rules separable, which matters because the two axes narrow in opposite directions.

## Cost of getting it wrong

Worth stating plainly, because it is the argument for `escalation: forbidden` existing at all. A paused attempt releases its serial segment but never integrates, so every dependent merge unit stays blocked behind it. In an unattended run that is a silent stall until a human notices. Failing is the better outcome: the attempt closes, the failure surfaces in `report`, and the unit is retryable within `max_attempts`.
