package workspace

import (
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"hash"
)

// AuthorityMaterial is resolved input supplied by an adapter. Exactly one pin
// form is valid for the declared Kind. ValidateDefinition copies Content before
// constructing an immutable AuthoritySnapshot.
type AuthorityMaterial struct {
	ID                   string
	Kind                 AuthorityKind
	Content              []byte
	RepositoryIdentity   string
	CommitObject         string
	BlobObject           string
	ExpectedSourceDigest string
}

type GitAuthorityPin struct {
	repository RepositoryIdentity
	commit     GitObjectID
	blob       GitObjectID
}

func (pin GitAuthorityPin) Repository() RepositoryIdentity { return pin.repository }
func (pin GitAuthorityPin) Commit() GitObjectID            { return pin.commit }
func (pin GitAuthorityPin) Blob() GitObjectID              { return pin.blob }

type AuthoritySnapshot struct {
	id           ID
	kind         AuthorityKind
	location     string
	sourceHash   Digest
	semanticHash Digest
	gitPin       GitAuthorityPin
	externalPin  Digest
}

func (snapshot AuthoritySnapshot) ID() ID               { return snapshot.id }
func (snapshot AuthoritySnapshot) Kind() AuthorityKind  { return snapshot.kind }
func (snapshot AuthoritySnapshot) Location() string     { return snapshot.location }
func (snapshot AuthoritySnapshot) SourceHash() Digest   { return snapshot.sourceHash }
func (snapshot AuthoritySnapshot) SemanticHash() Digest { return snapshot.semanticHash }
func (snapshot AuthoritySnapshot) GitPin() (GitAuthorityPin, bool) {
	return snapshot.gitPin, snapshot.kind == AuthorityGitBlob
}
func (snapshot AuthoritySnapshot) ExternalDigest() (Digest, bool) {
	return snapshot.externalPin, snapshot.kind == AuthorityExternalDigest
}

func pinAuthority(reference AuthorityReference, material AuthorityMaterial) (AuthoritySnapshot, error) {
	materialID, err := NewID(material.ID)
	if err != nil {
		return AuthoritySnapshot{}, fmt.Errorf("authority material id: %w", err)
	}
	if materialID != reference.id || material.Kind != reference.kind {
		return AuthoritySnapshot{}, fmt.Errorf("authority material %s does not match declared source %s", materialID, reference.id)
	}
	if len(material.Content) == 0 {
		return AuthoritySnapshot{}, fmt.Errorf("authority source %s is empty", reference.id)
	}
	if len(material.Content) > MaxArtifactBytes {
		return AuthoritySnapshot{}, fmt.Errorf("authority source %s exceeds %d bytes", reference.id, MaxArtifactBytes)
	}
	sourceHash := DigestBytes(append([]byte(nil), material.Content...))
	snapshot := AuthoritySnapshot{
		id: reference.id, kind: reference.kind, location: reference.location, sourceHash: sourceHash,
	}

	switch reference.kind {
	case AuthorityGitBlob:
		if material.ExpectedSourceDigest != "" {
			return AuthoritySnapshot{}, fmt.Errorf("git authority source %s cannot use an external digest pin", reference.id)
		}
		repository, err := NewRepositoryIdentity(material.RepositoryIdentity)
		if err != nil {
			return AuthoritySnapshot{}, fmt.Errorf("git authority source %s: %w", reference.id, err)
		}
		commit, err := ParseGitObjectID(material.CommitObject)
		if err != nil {
			return AuthoritySnapshot{}, fmt.Errorf("git authority source %s commit: %w", reference.id, err)
		}
		blob, err := ParseGitObjectID(material.BlobObject)
		if err != nil {
			return AuthoritySnapshot{}, fmt.Errorf("git authority source %s blob: %w", reference.id, err)
		}
		if commit.Algorithm() != blob.Algorithm() {
			return AuthoritySnapshot{}, fmt.Errorf("git authority source %s commit and blob use different object formats", reference.id)
		}
		computed, err := gitBlobObjectID(blob.Algorithm(), material.Content)
		if err != nil {
			return AuthoritySnapshot{}, err
		}
		if computed != blob {
			return AuthoritySnapshot{}, fmt.Errorf("git authority source %s content does not match pinned blob %s", reference.id, blob)
		}
		snapshot.gitPin = GitAuthorityPin{repository: repository, commit: commit, blob: blob}
	case AuthorityExternalDigest:
		if material.RepositoryIdentity != "" || material.CommitObject != "" || material.BlobObject != "" {
			return AuthoritySnapshot{}, fmt.Errorf("external authority source %s cannot use Git pins", reference.id)
		}
		expected, err := ParseDigest(material.ExpectedSourceDigest)
		if err != nil {
			return AuthoritySnapshot{}, fmt.Errorf("external authority source %s digest: %w", reference.id, err)
		}
		if expected != sourceHash {
			return AuthoritySnapshot{}, fmt.Errorf("external authority source %s content digest mismatch", reference.id)
		}
		snapshot.externalPin = expected
	default:
		return AuthoritySnapshot{}, fmt.Errorf("unsupported authority kind %q", reference.kind)
	}

	canonical, err := canonicalAuthorityBytes(snapshot)
	if err != nil {
		return AuthoritySnapshot{}, err
	}
	snapshot.semanticHash = DigestBytes(canonical)
	return snapshot, nil
}

func gitBlobObjectID(algorithm GitHashAlgorithm, content []byte) (GitObjectID, error) {
	var digest hash.Hash
	switch algorithm {
	case GitHashSHA1:
		digest = sha1.New() // Git object-format compatibility, not a security decision.
	case GitHashSHA256:
		digest = sha256.New()
	default:
		return GitObjectID{}, fmt.Errorf("unsupported Git blob algorithm %q", algorithm)
	}
	_, _ = fmt.Fprintf(digest, "blob %d%c", len(content), byte(0))
	_, _ = digest.Write(content)
	raw := digest.Sum(nil)
	var object GitObjectID
	object.algorithm = algorithm
	object.length = uint8(len(raw))
	copy(object.value[:], raw)
	return object, nil
}
