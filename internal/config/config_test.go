package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureGitignorePreservesContentAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(path, []byte("node_modules/\n.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	updated, err := EnsureGitignore(root)
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("expected .gitignore update")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range append([]string{"node_modules/"}, gitignoreEntries...) {
		if !strings.Contains(text, expected+"\n") {
			t.Fatalf("missing %q in %s", expected, text)
		}
	}
	if strings.Count("\n"+text, "\n.env\n") != 1 {
		t.Fatalf("duplicated existing entry: %s", text)
	}
	updated, err = EnsureGitignore(root)
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Fatal("second update should be a no-op")
	}
}
