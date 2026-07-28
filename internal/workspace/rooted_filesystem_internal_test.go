package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileExclusiveWithPreservesReplacementAfterPopulationFailure(
	t *testing.T,
) {
	rootPath, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := OpenRootedFilesystemAdapter(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	crash := errors.New("simulated population failure")
	target := filepath.Join(rootPath, "published.txt")
	original := filepath.Join(rootPath, "original.txt")
	err = adapter.writeFileExclusiveWith(
		"published.txt", 0o600,
		func(file *os.File) error {
			if _, err := file.Write([]byte("original\n")); err != nil {
				return err
			}
			if err := os.Rename(target, original); err != nil {
				return err
			}
			if err := os.WriteFile(
				target, []byte("replacement\n"), 0o600,
			); err != nil {
				return err
			}
			return crash
		},
	)
	if !errors.Is(err, crash) {
		t.Fatalf("population failure = %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "replacement\n" {
		t.Fatalf("replacement file changed or was removed: %q, %v", content, err)
	}
	content, err = os.ReadFile(original)
	if err != nil || string(content) != "original\n" {
		t.Fatalf("created file identity changed unexpectedly: %q, %v", content, err)
	}
}
