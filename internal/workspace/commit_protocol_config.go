package workspace

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxCommitSubjectBytes    = 998
	maxCommitBodyBytes       = 256 * 1024
	maxCommitProtocolSteps   = 256
	maxCommitChecksPerStep   = 128
	maxCommitPathPatterns    = 2048
	maxCheckFailureIDs       = 4096
	maxCheckFailureIDBytes   = 4096
	maxCheckCommandArguments = 1024
)

// CommitBodyPolicy controls the body independently from the exact subject.
// Exact bodies are useful when a commit is itself a protocol checkpoint;
// required and optional bodies are supplied to the imperative transaction.
type CommitBodyPolicy string

const (
	CommitBodyForbidden CommitBodyPolicy = "forbidden"
	CommitBodyOptional  CommitBodyPolicy = "optional"
	CommitBodyRequired  CommitBodyPolicy = "required"
	CommitBodyExact     CommitBodyPolicy = "exact"
)

func (policy CommitBodyPolicy) valid() bool {
	switch policy {
	case CommitBodyForbidden, CommitBodyOptional, CommitBodyRequired, CommitBodyExact:
		return true
	default:
		return false
	}
}

type CommitMessagePolicy struct {
	subject   string
	body      CommitBodyPolicy
	exactBody string
}

func NewCommitMessagePolicy(subject string, body CommitBodyPolicy, exactBody *string) (CommitMessagePolicy, error) {
	if err := validateCommitSubject(subject); err != nil {
		return CommitMessagePolicy{}, err
	}
	if !body.valid() {
		return CommitMessagePolicy{}, fmt.Errorf("unsupported commit body policy %q", body)
	}
	policy := CommitMessagePolicy{subject: subject, body: body}
	if body == CommitBodyExact {
		if exactBody == nil {
			return CommitMessagePolicy{}, fmt.Errorf("exact commit body policy requires exact_body")
		}
		if err := validateCommitBody(*exactBody); err != nil {
			return CommitMessagePolicy{}, fmt.Errorf("exact commit body: %w", err)
		}
		policy.exactBody = *exactBody
	} else if exactBody != nil {
		return CommitMessagePolicy{}, fmt.Errorf("exact_body is only valid with the exact body policy")
	}
	return policy, nil
}

func (policy CommitMessagePolicy) Subject() string              { return policy.subject }
func (policy CommitMessagePolicy) BodyPolicy() CommitBodyPolicy { return policy.body }
func (policy CommitMessagePolicy) ExactBody() string            { return policy.exactBody }

func (policy CommitMessagePolicy) ResolveBody(supplied string) (string, error) {
	if err := validateCommitBody(supplied); err != nil {
		return "", err
	}
	switch policy.body {
	case CommitBodyForbidden:
		if supplied != "" {
			return "", fmt.Errorf("commit body is forbidden")
		}
		return "", nil
	case CommitBodyOptional:
		return supplied, nil
	case CommitBodyRequired:
		if supplied == "" {
			return "", fmt.Errorf("commit body is required")
		}
		return supplied, nil
	case CommitBodyExact:
		if supplied != "" && supplied != policy.exactBody {
			return "", fmt.Errorf("supplied commit body does not match exact_body")
		}
		return policy.exactBody, nil
	default:
		return "", fmt.Errorf("invalid commit body policy %q", policy.body)
	}
}

func (policy CommitMessagePolicy) Validate(subject, body string) error {
	if subject != policy.subject {
		return fmt.Errorf("commit subject %q does not equal configured subject %q", subject, policy.subject)
	}
	if err := validateCommitBody(body); err != nil {
		return err
	}
	switch policy.body {
	case CommitBodyForbidden:
		if body != "" {
			return fmt.Errorf("commit body is forbidden")
		}
	case CommitBodyRequired:
		if body == "" {
			return fmt.Errorf("commit body is required")
		}
	case CommitBodyExact:
		if body != policy.exactBody {
			return fmt.Errorf("commit body does not equal exact_body")
		}
	case CommitBodyOptional:
	default:
		return fmt.Errorf("invalid commit body policy %q", policy.body)
	}
	return nil
}

func validateCommitSubject(subject string) error {
	if subject == "" || strings.TrimSpace(subject) != subject || len(subject) > maxCommitSubjectBytes ||
		!utf8.ValidString(subject) || strings.ContainsAny(subject, "\r\n\x00") {
		return fmt.Errorf("commit subject must be non-empty, single-line, canonical UTF-8 within %d bytes", maxCommitSubjectBytes)
	}
	return nil
}

func validateCommitBody(body string) error {
	if len(body) > maxCommitBodyBytes || !utf8.ValidString(body) || strings.IndexByte(body, 0) >= 0 ||
		strings.Contains(body, "\r") {
		return fmt.Errorf("commit body must be canonical UTF-8 within %d bytes", maxCommitBodyBytes)
	}
	if body != strings.TrimSuffix(body, "\n") || strings.HasPrefix(body, "\n") {
		return fmt.Errorf("commit body must not have leading or trailing blank framing")
	}
	return nil
}

// CommitPathPattern uses Git-style slash paths. A trailing /** is a recursive
// subtree; other patterns use path.Match. Frozen patterns always take
// precedence over allowed patterns, including both sides of a rename.
type CommitPathPattern struct {
	value     string
	recursive bool
	prefix    string
}

func NewCommitPathPattern(value string) (CommitPathPattern, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.Contains(value, "\\") ||
		strings.HasPrefix(value, "/") || strings.IndexByte(value, 0) >= 0 || !utf8.ValidString(value) {
		return CommitPathPattern{}, fmt.Errorf("invalid commit path pattern %q", value)
	}
	recursive := strings.HasSuffix(value, "/**")
	prefix := strings.TrimSuffix(value, "/**")
	if recursive && (prefix == "" || strings.ContainsAny(prefix, "*?[") || !portablePatternLiteral(prefix)) {
		return CommitPathPattern{}, fmt.Errorf("recursive commit path pattern %q requires a portable literal prefix", value)
	}
	if !recursive {
		if strings.Contains(value, "**") {
			return CommitPathPattern{}, fmt.Errorf("commit path pattern %q may use ** only as a trailing subtree", value)
		}
		if _, err := path.Match(value, "probe"); err != nil {
			return CommitPathPattern{}, fmt.Errorf("invalid commit path pattern %q: %w", value, err)
		}
		if !portablePatternSkeleton(value) {
			return CommitPathPattern{}, fmt.Errorf("commit path pattern %q is not portable", value)
		}
	}
	return CommitPathPattern{value: value, recursive: recursive, prefix: prefix}, nil
}

func (pattern CommitPathPattern) String() string { return pattern.value }

func (pattern CommitPathPattern) Matches(candidate string) bool {
	candidate, err := normalizeCommitPath(candidate)
	if err != nil {
		return false
	}
	if pattern.recursive {
		return candidate == pattern.prefix || strings.HasPrefix(candidate, pattern.prefix+"/")
	}
	matched, err := path.Match(pattern.value, candidate)
	return err == nil && matched
}

func portablePatternLiteral(value string) bool {
	if value == "" || strings.ContainsAny(value, "*?[") {
		return false
	}
	_, err := normalizeCommitPath(value)
	return err == nil
}

func portablePatternSkeleton(value string) bool {
	if strings.Contains(value, "//") || strings.HasSuffix(value, "/") || strings.Contains(value, ":") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." ||
			strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
			return false
		}
	}
	return true
}

func normalizeCommitPath(value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.Contains(value, "\\") ||
		strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\x00\r\n") || !utf8.ValidString(value) {
		return "", fmt.Errorf("invalid Git path %q", value)
	}
	cleaned := path.Clean(value)
	if cleaned != value || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") ||
		!isPortableRelativePath(cleaned) {
		return "", fmt.Errorf("Git path %q is not a canonical portable relative path", value)
	}
	return cleaned, nil
}

type CommitPathPolicy struct {
	allowed []CommitPathPattern
	frozen  []CommitPathPattern
	digest  Digest
}

func NewCommitPathPolicy(allowed, frozen []string) (CommitPathPolicy, error) {
	if len(allowed) == 0 {
		return CommitPathPolicy{}, fmt.Errorf("commit path policy requires at least one allowed path")
	}
	if len(allowed) > maxCommitPathPatterns || len(frozen) > maxCommitPathPatterns {
		return CommitPathPolicy{}, fmt.Errorf("commit path policy exceeds %d patterns", maxCommitPathPatterns)
	}
	parse := func(kind string, values []string) ([]CommitPathPattern, error) {
		result := make([]CommitPathPattern, 0, len(values))
		seen := make(map[string]struct{}, len(values))
		for index, value := range values {
			pattern, err := NewCommitPathPattern(value)
			if err != nil {
				return nil, fmt.Errorf("%s[%d]: %w", kind, index, err)
			}
			if _, exists := seen[pattern.value]; exists {
				return nil, fmt.Errorf("duplicate %s pattern %q", kind, pattern.value)
			}
			seen[pattern.value] = struct{}{}
			result = append(result, pattern)
		}
		return result, nil
	}
	allowedPatterns, err := parse("allowed_paths", allowed)
	if err != nil {
		return CommitPathPolicy{}, err
	}
	frozenPatterns, err := parse("frozen_paths", frozen)
	if err != nil {
		return CommitPathPolicy{}, err
	}
	type canonical struct {
		Allowed []string `json:"allowed_paths"`
		Frozen  []string `json:"frozen_paths"`
	}
	value := canonical{Allowed: append([]string{}, allowed...), Frozen: append([]string{}, frozen...)}
	content, _ := json.Marshal(value)
	return CommitPathPolicy{allowed: allowedPatterns, frozen: frozenPatterns, digest: DigestBytes(content)}, nil
}

func (policy CommitPathPolicy) Allowed() []CommitPathPattern {
	return append([]CommitPathPattern(nil), policy.allowed...)
}
func (policy CommitPathPolicy) Frozen() []CommitPathPattern {
	return append([]CommitPathPattern(nil), policy.frozen...)
}
func (policy CommitPathPolicy) Digest() Digest { return policy.digest }

func (policy CommitPathPolicy) Validate(pathValue string) error {
	normalized, err := normalizeCommitPath(pathValue)
	if err != nil {
		return err
	}
	for _, pattern := range policy.frozen {
		if pattern.Matches(normalized) {
			return fmt.Errorf("path %q is frozen by %q", normalized, pattern.value)
		}
	}
	for _, pattern := range policy.allowed {
		if pattern.Matches(normalized) {
			return nil
		}
	}
	return fmt.Errorf("path %q is outside configured allowed_paths", normalized)
}

type CheckExpectationKind string

const (
	CheckExpectationPass                CheckExpectationKind = "pass"
	CheckExpectationExpectedTestFailure CheckExpectationKind = "expected_test_failure"
)

func (kind CheckExpectationKind) valid() bool {
	return kind == CheckExpectationPass || kind == CheckExpectationExpectedTestFailure
}

type CheckExpectation struct {
	kind       CheckExpectationKind
	failureIDs []string
}

func NewCheckExpectation(kind CheckExpectationKind, failureIDs []string) (CheckExpectation, error) {
	if !kind.valid() {
		return CheckExpectation{}, fmt.Errorf("unsupported check expectation %q", kind)
	}
	if len(failureIDs) > maxCheckFailureIDs {
		return CheckExpectation{}, fmt.Errorf("check expectation exceeds %d failure identities", maxCheckFailureIDs)
	}
	values := append([]string(nil), failureIDs...)
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if value == "" || strings.TrimSpace(value) != value || len(value) > maxCheckFailureIDBytes ||
			!utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
			return CheckExpectation{}, fmt.Errorf("failure_ids[%d] is invalid", index)
		}
		if _, exists := seen[value]; exists {
			return CheckExpectation{}, fmt.Errorf("duplicate expected failure identity %q", value)
		}
		seen[value] = struct{}{}
	}
	sort.Strings(values)
	if kind == CheckExpectationPass && len(values) != 0 {
		return CheckExpectation{}, fmt.Errorf("pass expectation cannot include failure_ids")
	}
	if kind == CheckExpectationExpectedTestFailure && len(values) == 0 {
		return CheckExpectation{}, fmt.Errorf("expected_test_failure requires structured failure_ids")
	}
	return CheckExpectation{kind: kind, failureIDs: values}, nil
}

func (expectation CheckExpectation) Kind() CheckExpectationKind { return expectation.kind }
func (expectation CheckExpectation) FailureIDs() []string {
	return append([]string(nil), expectation.failureIDs...)
}

type CheckParserKind string

const (
	CheckParserGoTestJSON     CheckParserKind = "go-test-json"
	CheckParserAssertionJSON  CheckParserKind = "assertion-json"
	CheckParserDiagnosticJSON CheckParserKind = "diagnostic-json"
)

func (kind CheckParserKind) valid() bool {
	switch kind {
	case CheckParserGoTestJSON, CheckParserAssertionJSON, CheckParserDiagnosticJSON:
		return true
	default:
		return false
	}
}

type CommitCheck struct {
	id          ID
	runner      ID
	parser      CheckParserKind
	command     Argv
	expectation CheckExpectation
	digest      Digest
}

func NewCommitCheck(id, runner ID, parser CheckParserKind, command Argv, expectation CheckExpectation) (CommitCheck, error) {
	if id.IsZero() || runner.IsZero() || !parser.valid() || len(command.values) == 0 || !expectation.kind.valid() {
		return CommitCheck{}, fmt.Errorf("commit check requires id, runner, parser, command, and expectation")
	}
	if len(command.values) > maxCheckCommandArguments {
		return CommitCheck{}, fmt.Errorf("commit check command exceeds %d arguments", maxCheckCommandArguments)
	}
	if _, err := NewCheckExpectation(expectation.kind, expectation.failureIDs); err != nil {
		return CommitCheck{}, err
	}
	type canonical struct {
		ID          string               `json:"id"`
		Runner      string               `json:"runner"`
		Parser      CheckParserKind      `json:"parser"`
		Command     []string             `json:"command"`
		Expectation CheckExpectationKind `json:"expectation"`
		FailureIDs  []string             `json:"failure_ids"`
	}
	content, _ := json.Marshal(canonical{
		ID: id.String(), Runner: runner.String(), Parser: parser, Command: command.Values(),
		Expectation: expectation.kind, FailureIDs: expectation.FailureIDs(),
	})
	return CommitCheck{
		id: id, runner: runner, parser: parser, command: Argv{values: command.Values()},
		expectation: CheckExpectation{kind: expectation.kind, failureIDs: expectation.FailureIDs()},
		digest:      DigestBytes(content),
	}, nil
}

func (check CommitCheck) ID() ID                  { return check.id }
func (check CommitCheck) Runner() ID              { return check.runner }
func (check CommitCheck) Parser() CheckParserKind { return check.parser }
func (check CommitCheck) Command() Argv           { return Argv{values: check.command.Values()} }
func (check CommitCheck) Expectation() CheckExpectation {
	return CheckExpectation{kind: check.expectation.kind, failureIDs: check.expectation.FailureIDs()}
}
func (check CommitCheck) Digest() Digest { return check.digest }

type CommitStep struct {
	id      ID
	message CommitMessagePolicy
	paths   CommitPathPolicy
	checks  []CommitCheck
	digest  Digest
}

func NewCommitStep(id ID, message CommitMessagePolicy, paths CommitPathPolicy, checks []CommitCheck) (CommitStep, error) {
	if id.IsZero() || message.subject == "" || paths.digest.IsZero() {
		return CommitStep{}, fmt.Errorf("commit step requires id, message, and path policy")
	}
	if len(checks) > maxCommitChecksPerStep {
		return CommitStep{}, fmt.Errorf("commit step exceeds %d checks", maxCommitChecksPerStep)
	}
	copyChecks := append([]CommitCheck(nil), checks...)
	seen := make(map[string]struct{}, len(copyChecks))
	checkDigests := make([]string, 0, len(copyChecks))
	for _, check := range copyChecks {
		if check.id.IsZero() || check.digest.IsZero() {
			return CommitStep{}, fmt.Errorf("commit step contains an invalid check")
		}
		if _, exists := seen[check.id.String()]; exists {
			return CommitStep{}, fmt.Errorf("duplicate check id %s in commit step %s", check.id, id)
		}
		seen[check.id.String()] = struct{}{}
		checkDigests = append(checkDigests, check.digest.String())
	}
	type canonical struct {
		ID         string           `json:"id"`
		Subject    string           `json:"subject"`
		BodyPolicy CommitBodyPolicy `json:"body_policy"`
		ExactBody  string           `json:"exact_body,omitempty"`
		PathPolicy string           `json:"path_policy"`
		Checks     []string         `json:"checks"`
	}
	content, _ := json.Marshal(canonical{
		ID: id.String(), Subject: message.subject, BodyPolicy: message.body, ExactBody: message.exactBody,
		PathPolicy: paths.digest.String(), Checks: checkDigests,
	})
	return CommitStep{id: id, message: message, paths: paths, checks: copyChecks, digest: DigestBytes(content)}, nil
}

func (step CommitStep) ID() ID                       { return step.id }
func (step CommitStep) Message() CommitMessagePolicy { return step.message }
func (step CommitStep) Paths() CommitPathPolicy      { return cloneCommitPathPolicy(step.paths) }
func (step CommitStep) Checks() []CommitCheck        { return cloneCommitChecks(step.checks) }
func (step CommitStep) Digest() Digest               { return step.digest }

type CommitProtocol struct {
	steps  []CommitStep
	digest Digest
}

func NewCommitProtocol(steps []CommitStep) (CommitProtocol, error) {
	if len(steps) == 0 || len(steps) > maxCommitProtocolSteps {
		return CommitProtocol{}, fmt.Errorf("commit protocol requires 1..%d steps", maxCommitProtocolSteps)
	}
	copySteps := cloneCommitSteps(steps)
	ids := make(map[string]struct{}, len(copySteps))
	subjects := make(map[string]struct{}, len(copySteps))
	digests := make([]string, 0, len(copySteps))
	for _, step := range copySteps {
		if step.id.IsZero() || step.digest.IsZero() {
			return CommitProtocol{}, fmt.Errorf("commit protocol contains an invalid step")
		}
		if _, exists := ids[step.id.String()]; exists {
			return CommitProtocol{}, fmt.Errorf("duplicate commit step id %s", step.id)
		}
		if _, exists := subjects[step.message.subject]; exists {
			return CommitProtocol{}, fmt.Errorf("duplicate exact commit subject %q makes rebase mapping ambiguous", step.message.subject)
		}
		ids[step.id.String()] = struct{}{}
		subjects[step.message.subject] = struct{}{}
		digests = append(digests, step.digest.String())
	}
	content, _ := json.Marshal(struct {
		Steps []string `json:"steps"`
	}{Steps: digests})
	return CommitProtocol{steps: copySteps, digest: DigestBytes(content)}, nil
}

func (protocol CommitProtocol) Steps() []CommitStep { return cloneCommitSteps(protocol.steps) }
func (protocol CommitProtocol) Digest() Digest      { return protocol.digest }

// ReviewFixProtocol is intentionally separate from the implementation
// sequence. Each accepted fix gets a derived stable ordinal and consumes the
// merge unit's max_review_fixes budget while sharing this narrower rule.
type ReviewFixProtocol struct {
	subjectPrefix string
	body          CommitBodyPolicy
	paths         CommitPathPolicy
	checks        []CommitCheck
	digest        Digest
}

func NewReviewFixProtocol(subjectPrefix string, body CommitBodyPolicy, paths CommitPathPolicy, checks []CommitCheck) (ReviewFixProtocol, error) {
	if subjectPrefix == "" || strings.TrimSpace(subjectPrefix) != subjectPrefix ||
		len(subjectPrefix) > maxCommitSubjectBytes-24 || strings.ContainsAny(subjectPrefix, "\r\n\x00") || !utf8.ValidString(subjectPrefix) {
		return ReviewFixProtocol{}, fmt.Errorf("review-fix subject_prefix is invalid")
	}
	if body != CommitBodyForbidden && body != CommitBodyOptional && body != CommitBodyRequired {
		return ReviewFixProtocol{}, fmt.Errorf("review-fix body policy must be forbidden, optional, or required")
	}
	if paths.digest.IsZero() || len(checks) > maxCommitChecksPerStep {
		return ReviewFixProtocol{}, fmt.Errorf("review-fix protocol requires path policy and bounded checks")
	}
	copyChecks := cloneCommitChecks(checks)
	seen := make(map[string]struct{}, len(copyChecks))
	digests := make([]string, 0, len(copyChecks))
	for _, check := range copyChecks {
		if check.id.IsZero() || check.digest.IsZero() {
			return ReviewFixProtocol{}, fmt.Errorf("review-fix protocol contains an invalid check")
		}
		if _, exists := seen[check.id.String()]; exists {
			return ReviewFixProtocol{}, fmt.Errorf("duplicate review-fix check id %s", check.id)
		}
		seen[check.id.String()] = struct{}{}
		digests = append(digests, check.digest.String())
	}
	content, _ := json.Marshal(struct {
		SubjectPrefix string           `json:"subject_prefix"`
		Body          CommitBodyPolicy `json:"body_policy"`
		PathPolicy    string           `json:"path_policy"`
		Checks        []string         `json:"checks"`
	}{subjectPrefix, body, paths.digest.String(), digests})
	return ReviewFixProtocol{
		subjectPrefix: subjectPrefix, body: body, paths: paths,
		checks: copyChecks, digest: DigestBytes(content),
	}, nil
}

func (protocol ReviewFixProtocol) SubjectPrefix() string        { return protocol.subjectPrefix }
func (protocol ReviewFixProtocol) BodyPolicy() CommitBodyPolicy { return protocol.body }
func (protocol ReviewFixProtocol) Paths() CommitPathPolicy {
	return cloneCommitPathPolicy(protocol.paths)
}
func (protocol ReviewFixProtocol) Checks() []CommitCheck { return cloneCommitChecks(protocol.checks) }
func (protocol ReviewFixProtocol) Digest() Digest        { return protocol.digest }

func (protocol ReviewFixProtocol) Step(ordinal uint16) (CommitStep, error) {
	if ordinal == 0 {
		return CommitStep{}, fmt.Errorf("review-fix ordinal must be positive")
	}
	id, err := NewID(fmt.Sprintf("review-fix-%d", ordinal))
	if err != nil {
		return CommitStep{}, err
	}
	subject := fmt.Sprintf("%s %d", protocol.subjectPrefix, ordinal)
	message, err := NewCommitMessagePolicy(subject, protocol.body, nil)
	if err != nil {
		return CommitStep{}, err
	}
	return NewCommitStep(id, message, protocol.paths, protocol.checks)
}

func normalizeCommitProtocol(wire *commitProtocolWire, runner ID, location string) (*CommitProtocol, error) {
	if wire == nil {
		return nil, nil
	}
	steps := make([]CommitStep, 0, len(wire.Steps))
	for index, item := range wire.Steps {
		step, err := normalizeCommitStep(item, runner, fmt.Sprintf("%s.steps[%d]", location, index))
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	protocol, err := NewCommitProtocol(steps)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", location, err)
	}
	return &protocol, nil
}

func normalizeReviewFixProtocol(wire *reviewFixProtocolWire, runner ID, location string) (*ReviewFixProtocol, error) {
	if wire == nil {
		return nil, nil
	}
	if wire.AllowedPaths == nil || wire.FrozenPaths == nil || wire.Checks == nil {
		return nil, fmt.Errorf("%s must explicitly define allowed_paths, frozen_paths, and checks", location)
	}
	paths, err := NewCommitPathPolicy(*wire.AllowedPaths, *wire.FrozenPaths)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", location, err)
	}
	checks, err := normalizeCommitChecks(*wire.Checks, runner, location+".checks")
	if err != nil {
		return nil, err
	}
	protocol, err := NewReviewFixProtocol(wire.SubjectPrefix, CommitBodyPolicy(wire.BodyPolicy), paths, checks)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", location, err)
	}
	return &protocol, nil
}

func normalizeCommitStep(wire commitStepWire, runner ID, location string) (CommitStep, error) {
	id, err := NewID(wire.ID)
	if err != nil {
		return CommitStep{}, fmt.Errorf("%s.id: %w", location, err)
	}
	message, err := NewCommitMessagePolicy(wire.Subject, CommitBodyPolicy(wire.BodyPolicy), wire.ExactBody)
	if err != nil {
		return CommitStep{}, fmt.Errorf("%s: %w", location, err)
	}
	if wire.AllowedPaths == nil || wire.FrozenPaths == nil || wire.Checks == nil {
		return CommitStep{}, fmt.Errorf("%s must explicitly define allowed_paths, frozen_paths, and checks", location)
	}
	paths, err := NewCommitPathPolicy(*wire.AllowedPaths, *wire.FrozenPaths)
	if err != nil {
		return CommitStep{}, fmt.Errorf("%s: %w", location, err)
	}
	checks, err := normalizeCommitChecks(*wire.Checks, runner, location+".checks")
	if err != nil {
		return CommitStep{}, err
	}
	step, err := NewCommitStep(id, message, paths, checks)
	if err != nil {
		return CommitStep{}, fmt.Errorf("%s: %w", location, err)
	}
	return step, nil
}

func normalizeCommitChecks(wires []commitCheckWire, runner ID, location string) ([]CommitCheck, error) {
	if len(wires) > maxCommitChecksPerStep {
		return nil, fmt.Errorf("%s exceeds %d checks", location, maxCommitChecksPerStep)
	}
	result := make([]CommitCheck, 0, len(wires))
	for index, wire := range wires {
		checkID, err := NewID(wire.ID)
		if err != nil {
			return nil, fmt.Errorf("%s[%d].id: %w", location, index, err)
		}
		checkRunner, err := NewID(wire.Runner)
		if err != nil {
			return nil, fmt.Errorf("%s[%d].runner: %w", location, index, err)
		}
		if checkRunner != runner {
			return nil, fmt.Errorf("%s[%d] runner %s does not match profile runner %s", location, index, checkRunner, runner)
		}
		command, err := NewArgv(wire.Command...)
		if err != nil {
			return nil, fmt.Errorf("%s[%d].command: %w", location, index, err)
		}
		if wire.Expectation.FailureIDs == nil {
			return nil, fmt.Errorf("%s[%d].expectation.failure_ids must be explicit", location, index)
		}
		expectation, err := NewCheckExpectation(CheckExpectationKind(wire.Expectation.Kind), *wire.Expectation.FailureIDs)
		if err != nil {
			return nil, fmt.Errorf("%s[%d].expectation: %w", location, index, err)
		}
		check, err := NewCommitCheck(checkID, checkRunner, CheckParserKind(wire.Parser), command, expectation)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", location, index, err)
		}
		result = append(result, check)
	}
	return result, nil
}

func cloneCommitPathPolicy(policy CommitPathPolicy) CommitPathPolicy {
	policy.allowed = append([]CommitPathPattern(nil), policy.allowed...)
	policy.frozen = append([]CommitPathPattern(nil), policy.frozen...)
	return policy
}

func cloneCommitChecks(checks []CommitCheck) []CommitCheck {
	result := append([]CommitCheck(nil), checks...)
	for index := range result {
		result[index].command = Argv{values: result[index].command.Values()}
		result[index].expectation.failureIDs = result[index].expectation.FailureIDs()
	}
	return result
}

func cloneCommitSteps(steps []CommitStep) []CommitStep {
	result := append([]CommitStep(nil), steps...)
	for index := range result {
		result[index].paths = cloneCommitPathPolicy(result[index].paths)
		result[index].checks = cloneCommitChecks(result[index].checks)
	}
	return result
}

func cloneCommitProtocol(protocol *CommitProtocol) *CommitProtocol {
	if protocol == nil {
		return nil
	}
	copyProtocol := *protocol
	copyProtocol.steps = cloneCommitSteps(protocol.steps)
	return &copyProtocol
}

func cloneReviewFixProtocol(protocol *ReviewFixProtocol) *ReviewFixProtocol {
	if protocol == nil {
		return nil
	}
	copyProtocol := *protocol
	copyProtocol.paths = cloneCommitPathPolicy(protocol.paths)
	copyProtocol.checks = cloneCommitChecks(protocol.checks)
	return &copyProtocol
}
