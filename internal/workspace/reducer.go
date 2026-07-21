package workspace

import "fmt"

type CorePhase uint8

const (
	CoreEmpty CorePhase = iota
	CoreActive
	CorePaused
	CoreCompleted
)

type ReducerState struct {
	phase      CorePhase
	revision   uint64
	generation Digest
	evidence   []Evidence
}

func InitialReducerState() ReducerState { return ReducerState{phase: CoreEmpty} }

func (state ReducerState) Phase() CorePhase     { return state.phase }
func (state ReducerState) Revision() uint64     { return state.revision }
func (state ReducerState) Generation() Digest   { return state.generation }
func (state ReducerState) Evidence() []Evidence { return cloneEvidence(state.evidence) }

type CoreEvent interface {
	isCoreEvent()
}

type ActivateDefinition struct{ generation Digest }

func NewActivateDefinition(generation Digest) (ActivateDefinition, error) {
	if generation.IsZero() {
		return ActivateDefinition{}, fmt.Errorf("activation generation is required")
	}
	return ActivateDefinition{generation: generation}, nil
}
func (ActivateDefinition) isCoreEvent()             {}
func (event ActivateDefinition) Generation() Digest { return event.generation }

type PauseDefinition struct {
	reason   ID
	evidence []Evidence
}

func NewPauseDefinition(reason ID, evidence []Evidence) (PauseDefinition, error) {
	if reason.IsZero() {
		return PauseDefinition{}, fmt.Errorf("pause reason is required")
	}
	if err := validateEventEvidence(evidence); err != nil {
		return PauseDefinition{}, fmt.Errorf("pause evidence: %w", err)
	}
	return PauseDefinition{reason: reason, evidence: cloneEvidence(evidence)}, nil
}
func (PauseDefinition) isCoreEvent()               {}
func (event PauseDefinition) Reason() ID           { return event.reason }
func (event PauseDefinition) Evidence() []Evidence { return cloneEvidence(event.evidence) }

type ResumeDefinition struct{ generation Digest }

func NewResumeDefinition(generation Digest) (ResumeDefinition, error) {
	if generation.IsZero() {
		return ResumeDefinition{}, fmt.Errorf("resume generation is required")
	}
	return ResumeDefinition{generation: generation}, nil
}
func (ResumeDefinition) isCoreEvent()             {}
func (event ResumeDefinition) Generation() Digest { return event.generation }

type CompleteDefinition struct{ evidence []Evidence }

func NewCompleteDefinition(evidence []Evidence) (CompleteDefinition, error) {
	if err := validateEventEvidence(evidence); err != nil {
		return CompleteDefinition{}, fmt.Errorf("completion evidence: %w", err)
	}
	return CompleteDefinition{evidence: cloneEvidence(evidence)}, nil
}
func (CompleteDefinition) isCoreEvent() {}
func (event CompleteDefinition) Evidence() []Evidence {
	return cloneEvidence(event.evidence)
}

type Effect interface {
	isEffect()
}

type PersistProjectionEffect struct {
	revision   uint64
	generation Digest
}

func (PersistProjectionEffect) isEffect()                 {}
func (effect PersistProjectionEffect) Revision() uint64   { return effect.revision }
func (effect PersistProjectionEffect) Generation() Digest { return effect.generation }

type Directive interface {
	isDirective()
}

type PausedDirective struct {
	reason     ID
	revision   uint64
	generation Digest
}

func (PausedDirective) isDirective()                 {}
func (directive PausedDirective) Reason() ID         { return directive.reason }
func (directive PausedDirective) Revision() uint64   { return directive.revision }
func (directive PausedDirective) Generation() Digest { return directive.generation }

type Reduction struct {
	state      ReducerState
	effects    []Effect
	directives []Directive
}

func (reduction Reduction) State() ReducerState {
	return cloneReducerState(reduction.state)
}
func (reduction Reduction) Effects() []Effect { return append([]Effect(nil), reduction.effects...) }
func (reduction Reduction) Directives() []Directive {
	return append([]Directive(nil), reduction.directives...)
}

// Reduce is pure: it clones collection state, validates the transition, and
// returns closed effects/directives without calling any adapter.
func Reduce(current ReducerState, event CoreEvent) (Reduction, error) {
	next := cloneReducerState(current)
	if next.phase < CoreEmpty || next.phase > CoreCompleted {
		return Reduction{}, fmt.Errorf("invalid core phase %d", next.phase)
	}
	if event == nil {
		return Reduction{}, fmt.Errorf("core event is required")
	}

	var directives []Directive
	switch value := event.(type) {
	case ActivateDefinition:
		if current.phase != CoreEmpty || value.generation.IsZero() {
			return Reduction{}, fmt.Errorf("definition activation requires empty state and a generation")
		}
		next.phase = CoreActive
		next.generation = value.generation
	case PauseDefinition:
		if current.phase != CoreActive || value.reason.IsZero() {
			return Reduction{}, fmt.Errorf("definition pause requires active state and a reason")
		}
		if err := validateEventEvidence(value.evidence); err != nil {
			return Reduction{}, fmt.Errorf("definition pause has invalid evidence: %w", err)
		}
		next.phase = CorePaused
		next.evidence = append(next.evidence, cloneEvidence(value.evidence)...)
	case ResumeDefinition:
		if current.phase != CorePaused || value.generation != current.generation {
			return Reduction{}, fmt.Errorf("definition resume requires paused state and the active generation")
		}
		next.phase = CoreActive
	case CompleteDefinition:
		if current.phase != CoreActive {
			return Reduction{}, fmt.Errorf("definition completion requires active state")
		}
		if err := validateEventEvidence(value.evidence); err != nil {
			return Reduction{}, fmt.Errorf("definition completion has invalid evidence: %w", err)
		}
		next.phase = CoreCompleted
		next.evidence = append(next.evidence, cloneEvidence(value.evidence)...)
	default:
		return Reduction{}, fmt.Errorf("unsupported core event %T", event)
	}

	next.revision++
	effect := PersistProjectionEffect{revision: next.revision, generation: next.generation}
	if value, ok := event.(PauseDefinition); ok {
		directives = append(directives, PausedDirective{
			reason: value.reason, revision: next.revision, generation: next.generation,
		})
	}
	return Reduction{
		state: cloneReducerState(next), effects: []Effect{effect}, directives: directives,
	}, nil
}

func cloneReducerState(state ReducerState) ReducerState {
	state.evidence = cloneEvidence(state.evidence)
	return state
}

func validateEventEvidence(values []Evidence) error {
	for index, evidence := range values {
		if err := evidence.validate(); err != nil {
			return fmt.Errorf("item %d: %w", index, err)
		}
	}
	return nil
}
