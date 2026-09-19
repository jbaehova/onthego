package envfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFileDoesNotOverrideProcessEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("ONTHEGO_TEST_VALUE=from-file\nONTHEGO_QUOTED=\"hello world\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ONTHEGO_TEST_VALUE", "from-process")
	if err := LoadFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("ONTHEGO_TEST_VALUE"); got != "from-process" {
		t.Fatalf("process environment was overridden: %q", got)
	}
	if got := os.Getenv("ONTHEGO_QUOTED"); got != "hello world" {
		t.Fatalf("quoted value parsed as %q", got)
	}
}
