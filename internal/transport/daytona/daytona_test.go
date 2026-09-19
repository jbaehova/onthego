package daytona

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReceiverBinaryRequiresLinuxAMD64(t *testing.T) {
	path := filepath.Join(t.TempDir(), "onthego-linux-amd64")
	header := make([]byte, 20)
	copy(header[:4], []byte{0x7f, 'E', 'L', 'F'})
	header[18] = 0x3e
	if err := os.WriteFile(path, header, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ONTHEGO_RECEIVER_BINARY", path)
	got, err := receiverBinary()
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("receiver = %q, want %q", got, path)
	}

	invalid := filepath.Join(t.TempDir(), "onthego-darwin-arm64")
	if err := os.WriteFile(invalid, []byte("not an ELF binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ONTHEGO_RECEIVER_BINARY", invalid)
	if _, err := receiverBinary(); err == nil {
		t.Fatal("expected a platform validation error")
	}
}
