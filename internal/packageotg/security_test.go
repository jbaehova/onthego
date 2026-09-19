package packageotg

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/jbaehova/onthego/internal/otgerror"
	"github.com/klauspost/compress/zstd"
)

func TestExtractRejectsCiphertextTampering(t *testing.T) {
	base := t.TempDir()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "broken.otg")
	if err := os.WriteFile(path, []byte("not an age package"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Extract(path, filepath.Join(base, "out"), identity, "")
	var typed *otgerror.Error
	if !errors.As(err, &typed) || typed.Code != otgerror.CodePackageInvalid {
		t.Fatalf("expected E_PACKAGE_INVALID, got %v", err)
	}
}

func TestExtractRejectsPathTraversalBeforeWrite(t *testing.T) {
	base := t.TempDir()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "traversal.otg")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ageWriter, err := age.Encrypt(file, identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	zstdWriter, err := zstd.NewWriter(ageWriter)
	if err != nil {
		t.Fatal(err)
	}
	tarWriter := tar.NewWriter(zstdWriter)
	data := []byte("escaped")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "../escaped", Mode: 0o600, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(data); err != nil {
		t.Fatal(err)
	}
	for _, closer := range []interface{ Close() error }{tarWriter, zstdWriter, ageWriter, file} {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	_, err = Extract(path, filepath.Join(base, "out"), identity, "")
	var typed *otgerror.Error
	if !errors.As(err, &typed) || typed.Code != otgerror.CodePackageInvalid {
		t.Fatalf("expected E_PACKAGE_INVALID, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "escaped")); !os.IsNotExist(err) {
		t.Fatalf("path traversal wrote outside extraction root: %v", err)
	}
}
