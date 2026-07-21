package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const maxIdentifierBytes = 96

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

// ID is a normalized, immutable workspace-domain identifier.
type ID struct {
	value string
}

func NewID(value string) (ID, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return ID{}, fmt.Errorf("identifier is required")
	}
	if len(value) > maxIdentifierBytes || !utf8.ValidString(value) || !identifierPattern.MatchString(value) {
		return ID{}, fmt.Errorf("invalid identifier %q", value)
	}
	return ID{value: value}, nil
}

func MustID(value string) ID {
	id, err := NewID(value)
	if err != nil {
		panic(err)
	}
	return id
}

func (id ID) String() string { return id.value }
func (id ID) IsZero() bool   { return id.value == "" }

// Digest is an algorithm-qualified SHA-256 digest. The fixed-size value avoids
// retaining caller-owned byte slices.
type Digest struct {
	sum   [sha256.Size]byte
	valid bool
}

func DigestBytes(value []byte) Digest {
	return Digest{sum: sha256.Sum256(value), valid: true}
}

func ParseDigest(value string) (Digest, error) {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) {
		return Digest{}, fmt.Errorf("digest must use sha256")
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil || len(raw) != sha256.Size {
		return Digest{}, fmt.Errorf("invalid sha256 digest %q", value)
	}
	var sum [sha256.Size]byte
	copy(sum[:], raw)
	return Digest{sum: sum, valid: true}, nil
}

func (digest Digest) String() string {
	if !digest.valid {
		return ""
	}
	return "sha256:" + hex.EncodeToString(digest.sum[:])
}

func (digest Digest) Bytes() []byte {
	result := make([]byte, len(digest.sum))
	copy(result, digest.sum[:])
	return result
}

func (digest Digest) IsZero() bool { return !digest.valid }

type GitHashAlgorithm string

const (
	GitHashSHA1   GitHashAlgorithm = "sha1"
	GitHashSHA256 GitHashAlgorithm = "sha256"
)

// GitObjectID preserves the object-format algorithm instead of assuming SHA-1.
type GitObjectID struct {
	algorithm GitHashAlgorithm
	value     [sha256.Size]byte
	length    uint8
}

func ParseGitObjectID(value string) (GitObjectID, error) {
	prefix, encoded, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok {
		return GitObjectID{}, fmt.Errorf("git object id must be algorithm-qualified")
	}
	algorithm := GitHashAlgorithm(prefix)
	want := 0
	switch algorithm {
	case GitHashSHA1:
		want = 20
	case GitHashSHA256:
		want = sha256.Size
	default:
		return GitObjectID{}, fmt.Errorf("unsupported git object algorithm %q", prefix)
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil || len(raw) != want {
		return GitObjectID{}, fmt.Errorf("invalid %s git object id", algorithm)
	}
	var object GitObjectID
	object.algorithm = algorithm
	object.length = uint8(want)
	copy(object.value[:], raw)
	return object, nil
}

func (object GitObjectID) Algorithm() GitHashAlgorithm { return object.algorithm }

func (object GitObjectID) String() string {
	if object.length == 0 {
		return ""
	}
	return string(object.algorithm) + ":" + hex.EncodeToString(object.value[:object.length])
}

func (object GitObjectID) Bytes() []byte {
	result := make([]byte, int(object.length))
	copy(result, object.value[:object.length])
	return result
}

func (object GitObjectID) IsZero() bool { return object.length == 0 }

// RepositoryIdentity is a stable provider-independent repository identity.
type RepositoryIdentity struct {
	value string
}

func NewRepositoryIdentity(value string) (RepositoryIdentity, error) {
	value = strings.TrimSpace(value)
	if err := validateBoundedText("repository identity", value, 2048); err != nil {
		return RepositoryIdentity{}, err
	}
	return RepositoryIdentity{value: value}, nil
}

func (identity RepositoryIdentity) String() string { return identity.value }

// Argv is a typed process invocation. It never stores or renders a shell
// command string.
type Argv struct {
	values []string
}

func NewArgv(values ...string) (Argv, error) {
	if len(values) == 0 {
		return Argv{}, fmt.Errorf("argv requires an executable")
	}
	copyValues := append([]string(nil), values...)
	for index, value := range copyValues {
		if value == "" && index == 0 {
			return Argv{}, fmt.Errorf("argv executable is required")
		}
		if strings.IndexByte(value, 0) >= 0 || !utf8.ValidString(value) {
			return Argv{}, fmt.Errorf("argv item %d is invalid", index)
		}
	}
	return Argv{values: copyValues}, nil
}

func (argv Argv) Values() []string { return append([]string(nil), argv.values...) }

type EnvironmentVariable struct {
	name  string
	value string
}

func NewEnvironmentVariable(name, value string) (EnvironmentVariable, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "=\x00") {
		return EnvironmentVariable{}, fmt.Errorf("invalid environment variable name")
	}
	if strings.IndexByte(value, 0) >= 0 || !utf8.ValidString(value) {
		return EnvironmentVariable{}, fmt.Errorf("invalid environment variable value for %s", name)
	}
	return EnvironmentVariable{name: name, value: value}, nil
}

func (variable EnvironmentVariable) Name() string  { return variable.name }
func (variable EnvironmentVariable) Value() string { return variable.value }

type ReplayPolicy uint8

const (
	ReplayNever ReplayPolicy = iota + 1
	ReplayAfterVerifiedNoEffect
	ReplayIdempotently
)

func (policy ReplayPolicy) valid() bool {
	return policy >= ReplayNever && policy <= ReplayIdempotently
}

// Command is immutable process input for an imperative adapter.
type Command struct {
	argv        Argv
	directory   string
	environment []EnvironmentVariable
	replay      ReplayPolicy
}

func NewCommand(argv Argv, directory string, environment []EnvironmentVariable, replay ReplayPolicy) (Command, error) {
	if len(argv.values) == 0 {
		return Command{}, fmt.Errorf("command argv is required")
	}
	if !replay.valid() {
		return Command{}, fmt.Errorf("invalid replay policy")
	}
	if strings.IndexByte(directory, 0) >= 0 || !utf8.ValidString(directory) {
		return Command{}, fmt.Errorf("invalid command directory")
	}
	copyEnvironment := append([]EnvironmentVariable(nil), environment...)
	seen := make(map[string]struct{}, len(copyEnvironment))
	for _, variable := range copyEnvironment {
		if variable.name == "" {
			return Command{}, fmt.Errorf("command contains an invalid environment variable")
		}
		if _, exists := seen[variable.name]; exists {
			return Command{}, fmt.Errorf("duplicate environment variable %s", variable.name)
		}
		seen[variable.name] = struct{}{}
	}
	return Command{argv: Argv{values: argv.Values()}, directory: directory, environment: copyEnvironment, replay: replay}, nil
}

func (command Command) Argv() Argv {
	return Argv{values: command.argv.Values()}
}

func (command Command) Directory() string { return command.directory }

func (command Command) Environment() []EnvironmentVariable {
	return append([]EnvironmentVariable(nil), command.environment...)
}

func (command Command) ReplayPolicy() ReplayPolicy { return command.replay }

type EvidenceItem struct {
	name  ID
	value string
}

func NewEvidenceItem(name ID, value string) (EvidenceItem, error) {
	if name.IsZero() {
		return EvidenceItem{}, fmt.Errorf("evidence item name is required")
	}
	if err := validateBoundedText("evidence value", value, 16*1024); err != nil {
		return EvidenceItem{}, err
	}
	return EvidenceItem{name: name, value: value}, nil
}

func (item EvidenceItem) Name() ID      { return item.name }
func (item EvidenceItem) Value() string { return item.value }

type Evidence struct {
	kind   ID
	digest Digest
	items  []EvidenceItem
}

func NewEvidence(kind ID, digest Digest, items []EvidenceItem) (Evidence, error) {
	if kind.IsZero() || digest.IsZero() {
		return Evidence{}, fmt.Errorf("evidence kind and digest are required")
	}
	copyItems := append([]EvidenceItem(nil), items...)
	seen := make(map[string]struct{}, len(copyItems))
	for _, item := range copyItems {
		if item.name.IsZero() {
			return Evidence{}, fmt.Errorf("evidence item name is required")
		}
		if _, exists := seen[item.name.String()]; exists {
			return Evidence{}, fmt.Errorf("duplicate evidence item %s", item.name)
		}
		seen[item.name.String()] = struct{}{}
	}
	return Evidence{kind: kind, digest: digest, items: copyItems}, nil
}

func (evidence Evidence) Kind() ID       { return evidence.kind }
func (evidence Evidence) Digest() Digest { return evidence.digest }
func (evidence Evidence) Items() []EvidenceItem {
	return append([]EvidenceItem(nil), evidence.items...)
}

// Receipt is immutable signed evidence. Signature bytes are always copied.
type Receipt struct {
	keyID         ID
	payloadDigest Digest
	nonce         string
	expiresAt     time.Time
	signature     []byte
}

func NewReceipt(keyID ID, payloadDigest Digest, nonce string, expiresAt time.Time, signature []byte) (Receipt, error) {
	if keyID.IsZero() || payloadDigest.IsZero() {
		return Receipt{}, fmt.Errorf("receipt key and payload digest are required")
	}
	if err := validateBoundedText("receipt nonce", nonce, 512); err != nil {
		return Receipt{}, err
	}
	if expiresAt.IsZero() || len(signature) == 0 || len(signature) > 16*1024 {
		return Receipt{}, fmt.Errorf("receipt expiry and bounded signature are required")
	}
	return Receipt{
		keyID: keyID, payloadDigest: payloadDigest, nonce: nonce,
		expiresAt: expiresAt.UTC(), signature: append([]byte(nil), signature...),
	}, nil
}

func (receipt Receipt) KeyID() ID             { return receipt.keyID }
func (receipt Receipt) PayloadDigest() Digest { return receipt.payloadDigest }
func (receipt Receipt) Nonce() string         { return receipt.nonce }
func (receipt Receipt) ExpiresAt() time.Time  { return receipt.expiresAt }
func (receipt Receipt) Signature() []byte     { return append([]byte(nil), receipt.signature...) }

func validateBoundedText(name, value string, maxBytes int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > maxBytes || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s is invalid or exceeds %d bytes", name, maxBytes)
	}
	return nil
}
