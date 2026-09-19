package capture_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jbaehova/onthego/internal/capture"
	"github.com/jbaehova/onthego/internal/config"
	"github.com/jbaehova/onthego/internal/gitx"
	"github.com/jbaehova/onthego/internal/identity"
	"github.com/jbaehova/onthego/internal/restore"
)

func TestNamedEnvIsExcludedFromCapture(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init")
	git(t, root, "-c", "user.name=ONTHEGO Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture")
	mustWrite(t, filepath.Join(root, "vps.env"), []byte("PASSWORD=test-only\n"), 0o600)
	mustWrite(t, filepath.Join(root, "notes.txt"), []byte("safe\n"), 0o644)
	cfg := config.Default()
	cfg.Include = nil
	files, err := capture.Preview(context.Background(), capture.Options{Root: root, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].RelativePath != "notes.txt" {
		t.Fatalf("unexpected captured files: %#v", files)
	}
	_, err = capture.Preview(context.Background(), capture.Options{Root: root, Config: cfg, Include: []string{"vps.env"}})
	if err == nil || !strings.Contains(err.Error(), "secret candidate") {
		t.Fatalf("expected explicit include to require secret opt-in, got %v", err)
	}
}

func TestCaptureRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	t.Setenv("ONTHEGO_DATA_HOME", filepath.Join(base, "data"))
	root := filepath.Join(base, "source")
	mustMkdir(t, filepath.Join(root, "evidence"))
	mustWrite(t, filepath.Join(root, ".gitignore"), []byte("evidence/ignored.bin\n"), 0o644)
	mustWrite(t, filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o644)
	mustWrite(t, filepath.Join(root, "deleted.txt"), []byte("delete me\n"), 0o644)
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "ONTHEGO Test")
	git(t, root, "config", "user.email", "test@example.invalid")
	git(t, root, "add", ".gitignore", "tracked.txt", "deleted.txt")
	git(t, root, "commit", "-m", "fixture")

	mustWrite(t, filepath.Join(root, "tracked.txt"), []byte("staged\n"), 0o644)
	git(t, root, "add", "tracked.txt")
	mustWrite(t, filepath.Join(root, "tracked.txt"), []byte("unstaged\n"), 0o644)
	mustWrite(t, filepath.Join(root, "added.txt"), []byte("new staged file\n"), 0o644)
	git(t, root, "add", "added.txt")
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "한국어-메모.txt"), []byte("작업 맥락\n"), 0o755)
	binary := []byte{0, 1, 2, 0xff, 0, 7}
	mustWrite(t, filepath.Join(root, "evidence", "ignored.bin"), binary, 0o644)

	cfg := config.Default()
	cfg.Include = []string{config.FileName, "evidence/ignored.bin"}
	if err := config.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	contextDir := filepath.Join(base, "context")
	mustMkdir(t, contextDir)
	mustWrite(t, filepath.Join(contextDir, "session.jsonl"), []byte("{\"message\":\"redacted context\"}\n"), 0o600)
	mustWrite(t, filepath.Join(contextDir, "HANDOFF.md"), []byte("# Handoff\n"), 0o600)
	mustWrite(t, filepath.Join(contextDir, "context-envelope.json"), []byte(`{"schema_version":1,"id":"ctx-1","project_id":"`+cfg.ProjectID+`","agent_kind":"codex","session_id":"session-1"}`), 0o600)

	keys, err := identity.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	packagePath := filepath.Join(base, "snapshot.otg")
	result, err := capture.Create(ctx, capture.Options{Root: root, Config: cfg, Output: packagePath, ContextDir: contextDir, EnvironmentID: "local", OnthegoVersion: "test", Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.Agent.ContextEnvelopeID != "ctx-1" {
		t.Fatalf("context envelope was not recorded: %#v", result.Manifest.Agent)
	}
	ageIdentity, err := keys.Identity()
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(base, "restored")
	if _, err := restore.Package(ctx, packagePath, destination, ageIdentity, keys.SigningPublic); err != nil {
		t.Fatal(err)
	}

	assertGitOutput(t, ctx, root, destination, "diff", "--cached", "--binary", "--full-index", "--no-ext-diff", "--no-textconv")
	assertGitOutput(t, ctx, root, destination, "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv")
	for _, path := range []string{"tracked.txt", "added.txt", "한국어-메모.txt", "evidence/ignored.bin", config.FileName} {
		left, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		right, err := os.ReadFile(filepath.Join(destination, path))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(left, right) {
			t.Fatalf("%s differs after restore", path)
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "deleted.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted tracked file was restored: %v", err)
	}
	info, err := os.Stat(filepath.Join(destination, "한국어-메모.txt"))
	if err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("executable mode was not preserved: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".onthego.local", "context", "HANDOFF.md")); err != nil {
		t.Fatalf("context envelope missing after restore: %v", err)
	}
}

func assertGitOutput(t *testing.T, ctx context.Context, left, right string, args ...string) {
	t.Helper()
	l, err := gitx.Run(ctx, left, args...)
	if err != nil {
		t.Fatal(err)
	}
	r, err := gitx.Run(ctx, right, args...)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(l.Stdout, r.Stdout) {
		t.Fatalf("git %v output differs\nsource:\n%s\nrestored:\n%s", args, l.Stdout, r.Stdout)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}
