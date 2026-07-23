package workspace

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
)

type PublicationFaultPoint string

const (
	PublicationFaultAfterIntent     PublicationFaultPoint = "after_intent"
	PublicationFaultAfterPending    PublicationFaultPoint = "after_pending"
	PublicationFaultAfterQuarantine PublicationFaultPoint = "after_quarantine"
	PublicationFaultAfterPublish    PublicationFaultPoint = "after_publish"
)

type PublicationOptions struct {
	FaultInjector func(PublicationFaultPoint) error
}

type replaceablePublicationWire struct {
	SchemaVersion int    `json:"schema_version"`
	Target        string `json:"target"`
	Pending       string `json:"pending"`
	Backup        string `json:"backup"`
	OldDigest     string `json:"old_digest,omitempty"`
	NewDigest     string `json:"new_digest"`
	Maximum       int64  `json:"maximum"`
}

// PublishReplaceable durably replaces one regular file without ever replacing
// a pathname in a single rename. The old object is first quarantined with a
// no-replace rename under a synced intent; the new object is then published
// with another no-replace rename. Any interrupted state is completed or rolled
// back before a subsequent read or publication.
func (root *VerifiedRoot) PublishReplaceable(
	relative string,
	content []byte,
	permission os.FileMode,
	maximum int64,
	options PublicationOptions,
) error {
	if root == nil || root.adapter == nil {
		return fmt.Errorf("verified root is closed")
	}
	if maximum < 0 || int64(len(content)) > maximum {
		return fmt.Errorf("replaceable rooted file %s exceeds %d bytes", relative, maximum)
	}
	rooted, err := NewRootedPath(root.path, relative)
	if err != nil {
		return err
	}
	relative = rooted.Relative()
	if err := root.RecoverReplaceable(relative); err != nil {
		return err
	}

	oldContent, readErr := root.ReadBounded(relative, maximum)
	oldExists := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if oldExists && bytes.Equal(oldContent, content) {
		return root.Sync()
	}

	intentPath, pendingPath, backupPath := publicationControlPaths(relative)
	wire := replaceablePublicationWire{
		SchemaVersion: RuntimeFormatSchemaVersion,
		Target:        relative,
		Pending:       pendingPath,
		Backup:        backupPath,
		NewDigest:     DigestBytes(content).String(),
		Maximum:       maximum,
	}
	if oldExists {
		wire.OldDigest = DigestBytes(oldContent).String()
	}
	intentBytes, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	if err := root.WriteExclusive(intentPath, intentBytes, 0o600); err != nil {
		return fmt.Errorf("prepare replaceable publication for %s: %w", relative, err)
	}
	if err := injectPublicationFault(options, PublicationFaultAfterIntent); err != nil {
		return err
	}
	if err := root.WriteExclusive(pendingPath, content, permission); err != nil {
		return fmt.Errorf("write replaceable publication for %s: %w", relative, err)
	}
	if err := injectPublicationFault(options, PublicationFaultAfterPending); err != nil {
		return err
	}
	if oldExists {
		if err := root.adapter.renameFileNoReplace(relative, backupPath); err != nil {
			return fmt.Errorf("quarantine prior rooted file %s: %w", relative, err)
		}
		if err := root.VerifyPath(); err != nil {
			return err
		}
	}
	if err := injectPublicationFault(options, PublicationFaultAfterQuarantine); err != nil {
		return err
	}
	if err := root.adapter.renameFileNoReplace(pendingPath, relative); err != nil {
		return fmt.Errorf("publish replaceable rooted file %s: %w", relative, err)
	}
	if err := injectPublicationFault(options, PublicationFaultAfterPublish); err != nil {
		return err
	}
	if err := root.finishReplaceablePublication(wire, intentBytes); err != nil {
		return err
	}
	published, err := root.ReadBounded(relative, maximum)
	if err != nil {
		return err
	}
	if !bytes.Equal(published, content) {
		return fmt.Errorf("replaceable rooted file %s does not match its publication", relative)
	}
	return nil
}

// ReadReplaceable recovers a previously interrupted publication before
// returning the current stable file.
func (root *VerifiedRoot) ReadReplaceable(relative string, maximum int64) ([]byte, error) {
	if err := root.RecoverReplaceable(relative); err != nil {
		return nil, err
	}
	return root.ReadBounded(relative, maximum)
}

func (root *VerifiedRoot) RecoverReplaceable(relative string) error {
	if root == nil || root.adapter == nil {
		return fmt.Errorf("verified root is closed")
	}
	rooted, err := NewRootedPath(root.path, relative)
	if err != nil {
		return err
	}
	relative = rooted.Relative()
	intentPath, expectedPending, expectedBackup := publicationControlPaths(relative)
	intentBytes, err := root.ReadBounded(intentPath, 16*1024)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read replaceable publication intent for %s: %w", relative, err)
	}
	var wire replaceablePublicationWire
	if err := decodeStrictJSON(intentBytes, &wire); err != nil {
		return fmt.Errorf("decode replaceable publication intent for %s: %w", relative, err)
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, intentBytes) ||
		wire.SchemaVersion != RuntimeFormatSchemaVersion ||
		wire.Target != relative ||
		wire.Pending != expectedPending ||
		wire.Backup != expectedBackup ||
		wire.Maximum < 0 {
		return fmt.Errorf("replaceable publication intent for %s is invalid", relative)
	}
	newDigest, err := ParseDigest(wire.NewDigest)
	if err != nil {
		return fmt.Errorf("replaceable publication new digest for %s: %w", relative, err)
	}
	var oldDigest Digest
	if wire.OldDigest != "" {
		oldDigest, err = ParseDigest(wire.OldDigest)
		if err != nil {
			return fmt.Errorf("replaceable publication old digest for %s: %w", relative, err)
		}
	}

	targetKind, err := root.classifyPublicationFile(relative, wire.Maximum, oldDigest, newDigest)
	if err != nil {
		return err
	}
	pendingKind, err := root.classifyPublicationFile(wire.Pending, wire.Maximum, Digest{}, newDigest)
	if err != nil {
		return err
	}
	backupKind, err := root.classifyPublicationFile(wire.Backup, wire.Maximum, oldDigest, Digest{})
	if err != nil {
		return err
	}

	switch {
	case targetKind == publicationNew:
		// Publication completed; only durable cleanup remains.
	case targetKind == publicationOld && pendingKind == publicationNew && backupKind == publicationMissing:
		if err := root.adapter.renameFileNoReplace(relative, wire.Backup); err != nil {
			return fmt.Errorf("resume quarantine for %s: %w", relative, err)
		}
		if err := root.adapter.renameFileNoReplace(wire.Pending, relative); err != nil {
			return fmt.Errorf("resume publication for %s: %w", relative, err)
		}
	case targetKind == publicationMissing && pendingKind == publicationNew && backupKind == publicationOld:
		if err := root.adapter.renameFileNoReplace(wire.Pending, relative); err != nil {
			return fmt.Errorf("resume publication for %s: %w", relative, err)
		}
	case targetKind == publicationMissing && pendingKind == publicationNew &&
		backupKind == publicationMissing && wire.OldDigest == "":
		if err := root.adapter.renameFileNoReplace(wire.Pending, relative); err != nil {
			return fmt.Errorf("resume initial publication for %s: %w", relative, err)
		}
	case targetKind == publicationOld && pendingKind == publicationMissing &&
		backupKind == publicationMissing:
		// The new object was never made durable. Roll back the intent.
		return root.removePublicationIntent(intentPath, intentBytes)
	case targetKind == publicationMissing && pendingKind == publicationMissing &&
		backupKind == publicationOld:
		// The old object was quarantined but the new object was not durable.
		if err := root.adapter.renameFileNoReplace(wire.Backup, relative); err != nil {
			return fmt.Errorf("roll back publication for %s: %w", relative, err)
		}
		return root.removePublicationIntent(intentPath, intentBytes)
	case targetKind == publicationMissing && pendingKind == publicationMissing &&
		backupKind == publicationMissing && wire.OldDigest == "":
		return root.removePublicationIntent(intentPath, intentBytes)
	default:
		return fmt.Errorf(
			"replaceable publication for %s has unsafe state target=%s pending=%s backup=%s",
			relative, targetKind, pendingKind, backupKind,
		)
	}
	return root.finishReplaceablePublication(wire, intentBytes)
}

type publicationFileKind string

const (
	publicationMissing publicationFileKind = "missing"
	publicationOld     publicationFileKind = "old"
	publicationNew     publicationFileKind = "new"
)

func (root *VerifiedRoot) classifyPublicationFile(
	relative string,
	maximum int64,
	oldDigest Digest,
	newDigest Digest,
) (publicationFileKind, error) {
	content, err := root.ReadBounded(relative, maximum)
	if errors.Is(err, os.ErrNotExist) {
		return publicationMissing, nil
	}
	if err != nil {
		return "", err
	}
	digest := DigestBytes(content)
	if !newDigest.IsZero() && digest == newDigest {
		return publicationNew, nil
	}
	if !oldDigest.IsZero() && digest == oldDigest {
		return publicationOld, nil
	}
	return "", fmt.Errorf("replaceable publication file %s has an unexpected digest %s", relative, digest)
}

func (root *VerifiedRoot) finishReplaceablePublication(
	wire replaceablePublicationWire,
	intentBytes []byte,
) error {
	target, err := root.ReadBounded(wire.Target, wire.Maximum)
	if err != nil {
		return err
	}
	if DigestBytes(target).String() != wire.NewDigest {
		return fmt.Errorf("replaceable publication target %s does not match its new digest", wire.Target)
	}
	if wire.OldDigest != "" {
		if _, err := root.adapter.removeFileHashExact(
			wire.Backup, wire.OldDigest, wire.Maximum, root.VerifyPath,
		); err != nil {
			return fmt.Errorf("remove replaceable publication backup for %s: %w", wire.Target, err)
		}
	}
	if _, err := root.adapter.removeFileHashExact(
		wire.Pending, wire.NewDigest, wire.Maximum, root.VerifyPath,
	); err != nil {
		return fmt.Errorf("remove replaceable publication pending file for %s: %w", wire.Target, err)
	}
	intentPath, _, _ := publicationControlPaths(wire.Target)
	return root.removePublicationIntent(intentPath, intentBytes)
}

func (root *VerifiedRoot) removePublicationIntent(intentPath string, intentBytes []byte) error {
	removed, err := root.adapter.removeFileContentExact(
		intentPath, intentBytes, int64(len(intentBytes)), root.VerifyPath,
	)
	if err != nil {
		return fmt.Errorf("remove replaceable publication intent %s: %w", intentPath, err)
	}
	if !removed {
		return fmt.Errorf("replaceable publication intent %s disappeared", intentPath)
	}
	return root.Sync()
}

func publicationControlPaths(relative string) (intent string, pending string, backup string) {
	parent := path.Dir(relative)
	key := hex.EncodeToString(DigestBytes([]byte(relative)).Bytes())[:20]
	prefix := "runtime-publication-" + key
	return path.Join(parent, prefix+".intent.json"),
		path.Join(parent, prefix+".new"),
		path.Join(parent, prefix+".old")
}

func injectPublicationFault(options PublicationOptions, point PublicationFaultPoint) error {
	if options.FaultInjector == nil {
		return nil
	}
	if err := options.FaultInjector(point); err != nil {
		return fmt.Errorf("replaceable publication fault at %s: %w", point, err)
	}
	return nil
}
