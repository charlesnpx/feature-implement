# Boundary Checkpoints and Escalation

Status: Implemented.

This design record describes the implemented split of `AttemptBoundaryMode`
into two fields. It lets blocks of merge units run to completion without
stopping while making planned owner checkpoints and agent-raised exception
stops explicit and enforceable.

## Problem resolved

The former `boundary.mode` carried two unrelated jobs in one enum.

**Planned checkpoint.** The owner, ahead of time, says "after this unit, stop and tell me." Nothing is wrong; the owner just wants a decision point there.

**Exception stop.** The agent, at runtime, says "this went sideways" or "a decision came up that isn't mine to make." Unschedulable by definition — if it could be predicted, it would be a checkpoint.

Configuration can only express the first. That is why a value like
`no_pause_unless_problem` cannot work: "problem" is a judgment made hours after
the configuration was read, and no enum value can evaluate it.

The conflation had two consequences:

- Every merge unit had to declare a mode, so a unit that never intended to pause
  still wrote `mode: pause_only` — a false promise to pause. The configuration
  could not say that a block runs unattended.
- Whether an attempt stopped was decided entirely by whether the runner called
  `attempt boundary`, rather than by explicit workflow guidance tied to policy.

## Shipped schema

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

`checkpoint` is the owner's planned gate. `escalation` is the agent's
permission to stop on its own.

### Semantics

| checkpoint | escalation | Behavior |
|---|---|---|
| `none` | `allowed` | Runs unit to unit without stopping. The agent may still stop if it hits something real. **This is the block-of-units default.** |
| `none` | `forbidden` | Cannot stop for any reason. The agent must finish or fail. For unattended and CI runs. |
| `pause_only` | `allowed` | Stops at this unit, owner responds `continue`, resumes on the same goal. |
| `complete_goal_and_wait` | `allowed` | Goal is finished here; owner responds, a next goal is reserved and acked, attempt resumes on the new goal. |

`checkpoint: none` makes an unattended block expressible.

## Boundary kind and reporting

Both arrive through `attempt boundary`, whose input requires `kind` with no
default:

- **`checkpoint`** — legal only when `checkpoint != none`; it takes the shape
  of the configured checkpoint value.
- **`escalation`** — legal only when `escalation: allowed`; it always takes the
  `pause_only` shape, so an exception stop resumes the same goal rather than
  handing it off.

The recorded kind is carried on `AttemptBoundaryReachedJournalEvent`, folded
into the directive digest, and exposed by `status` and `report` as
`boundary_kind` alongside the directive `kind`.

## Implemented enforcement

| Location | Shipped behavior |
|---|---|
| `internal/workspace/wire.go` | Merge-unit boundaries decode `checkpoint`, `escalation`, and unchanged `serial_segment`; profile boundaries decode `escalation` only. |
| `internal/workspace/policy.go` | `AttemptBoundaryPolicy` carries checkpoint, escalation, and serial segment; `ProfileBoundaryPolicy` carries escalation only. |
| `internal/workspace/attempt_lifecycle.go` | Reserve binds both fields. `RecordAttemptBoundary` rejects a checkpoint on `checkpoint: none` and an escalation on `escalation: forbidden` before appending. |
| `internal/workspace/attempt_projection.go` | Boundary projection validates the recorded kind against the reserved policy. A `pause_only` shape resumes the same goal; `complete_goal_and_wait` requires the acknowledged next goal. |
| Journal codec and events | The kind is serialized, reconstructed, and included in directive bindings. |
| `internal/workspace/runtime_views.go` | `status` and `report` expose `boundary_kind`. |

## Profile narrowing

An execution profile may optionally declare `boundary` with `escalation` only.
`escalation` is an agent permission, so a merge unit may narrow `allowed` to
`forbidden` but may never widen `forbidden` to `allowed`.

The proposed profile-level `checkpoint` was withdrawn. `pause_only` and
`complete_goal_and_wait` do not order against each other: one keeps the goal
and the other requires a new one. A profile checkpoint would therefore pin each
unit beneath it to exactly one shape.

## Format and configuration migration

The current local runtime marker is `feature.runtime.v5.json`. A root with the
old `feature.runtime.v4.json` marker is rejected at the format gate with the
existing regeneration diagnostic; v4 is not interpreted or migrated.

The execution-config `schema_version` remains 2. The earlier proposal to bump
it was rejected as disproportionate: `decodeStrictV2` gates every strict
document, including plan sources and the workspace bundle, so a bump would
rewrite testdata across three formats for a change confined to one nested block
of one format. Existing boundary entries translate mechanically:

```
mode: pause_only              →  checkpoint: pause_only
                                 escalation: allowed
mode: complete_goal_and_wait  →  checkpoint: complete_goal_and_wait
                                 escalation: allowed
```

That translation is behavior-preserving. Adopting `checkpoint: none` on units
that should run unattended is a separate, deliberate edit.

`mode` is not retained as a decode-time alias; strict decoding rejects it and
directs callers to `checkpoint` and `escalation`.

## Runner ordering and conditionality

The runner records a boundary only for a configured checkpoint other than
`none` or a genuine allowed escalation. It records that boundary before
`integrate merge-unit`, while the attempt is active, then resolves directives
and resumes before integration. A unit with no needed boundary proceeds
directly to integration.

Integration sets the attempt to `AttemptCompleted` and clears its lease:

```go
updated.phase = AttemptCompleted
updated.serialSegmentHeld = false
updated.leaseID = ID{}
```

`RecordAttemptBoundary` requires `AttemptActive`, so a boundary cannot be
recorded after integration; it returns `attempt %s must be active to reach a
boundary`.

## Deliberate follow-up

`serial_segment` still lives under `boundary`, although reserve-time scheduling
consumes it regardless of pauses. It should be lifted to the merge-unit level
in a follow-up; this implementation deliberately leaves it in place.

## Why not a single richer enum

An `escalation` value folded into `checkpoint` would produce combinations that
do not mean anything — a planned goal-handoff checkpoint that also forbids
exception stops is coherent, but expressing it needs both axes anyway. Two
fields with two and three values beat one field with six, and they keep planned
gates separate from agent permissions.

## Cost of getting it wrong

Worth stating plainly, because it is the argument for `escalation: forbidden` existing at all. A paused attempt releases its serial segment but never integrates, so every dependent merge unit stays blocked behind it. In an unattended run that is a silent stall until a human notices. Failing is the better outcome: the attempt closes, the failure surfaces in `report`, and the unit is retryable within `max_attempts`.
