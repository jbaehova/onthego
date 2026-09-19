package contextenv

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestDiscoveryIsProjectScopedAndExportRedactsSecrets(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "project")
	other := filepath.Join(base, "other")
	sessions := filepath.Join(base, "sessions")
	for _, path := range []string{project, other, sessions} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	matching := []byte(`{"session_id":"matching","cwd":"` + project + `","token":"hf_12345678901234567890","auth":"Bearer abcdefghijklmnopqrstuvwxyz","encrypted_content":"cipher-sk-abcdefghijklmnop"}` + "\n")
	foreign := []byte(`{"session_id":"foreign","cwd":"` + other + `"}` + "\n")
	if err := os.WriteFile(filepath.Join(sessions, "matching.jsonl"), matching, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "foreign.jsonl"), foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	discovered, err := DiscoverCodexAt(project, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered) != 1 || discovered[0].ID != "matching" {
		t.Fatalf("unexpected project-scoped sessions: %#v", discovered)
	}
	result, err := Export("project-1", project, "test", discovered[0], handoffInput("continue task"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(result.Log, []byte("hf_123456")) || bytes.Contains(result.Log, []byte("abcdefghijklmnopqrstuvwxyz")) || bytes.Contains(result.Log, []byte("cipher-sk-")) {
		t.Fatalf("secret remained in redacted log: %s", result.Log)
	}
	if len(result.Envelope.Redactions) < 2 {
		t.Fatalf("expected redaction evidence, got %#v", result.Envelope.Redactions)
	}
}

func TestDiscoverAllAgentsAndExportEveryMatchingSession(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "workspace", "demo")
	roots := Roots{
		CodexSessions:  filepath.Join(base, "codex"),
		CursorProjects: filepath.Join(base, "cursor"),
		ClaudeProjects: filepath.Join(base, "claude"),
		HermesHome:     filepath.Join(base, "hermes"),
	}
	for _, path := range []string{project, roots.CodexSessions, filepath.Join(roots.HermesHome, "sessions")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cursorProject := projectDirectories(roots.CursorProjects, project, false)[0]
	claudeProject := projectDirectories(roots.ClaudeProjects, project, true)[1]
	for _, path := range []string{filepath.Join(cursorProject, "agent-transcripts", "cursor-1"), claudeProject} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(roots.CodexSessions, "codex-1.jsonl"):                             `{"session_id":"codex-1","cwd":"` + project + `","token":"npm_abcdefghijklmnopqrstuvwxyz123456"}` + "\n",
		filepath.Join(cursorProject, "agent-transcripts", "cursor-1", "cursor-1.jsonl"): `{"role":"user","message":{"content":"cursor work"}}` + "\n",
		filepath.Join(claudeProject, "claude-1.jsonl"):                                  `{"sessionId":"claude-1","cwd":"` + project + `","message":"claude work"}` + "\n",
		filepath.Join(roots.HermesHome, "sessions", "hermes-1.jsonl"):                   `{"session_id":"hermes-1","workspace":"` + project + `","message":"hermes work"}` + "\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := DiscoverAllAt(project, roots)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 4 {
		t.Fatalf("sessions = %#v", sessions)
	}
	if got := Kinds(sessions); !equalStrings(got, []string{"claude", "codex", "cursor", "hermes"}) {
		t.Fatalf("agent kinds = %#v", got)
	}
	result, err := ExportAll("project-1", project, "test", sessions, handoffInput("continue everything"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Logs) != 4 || len(result.Envelope.Sessions) != 4 {
		t.Fatalf("exported sessions = %#v", result.Envelope.Sessions)
	}
	for _, data := range result.Logs {
		if bytes.Contains(data, []byte("npm_abcdefghijklmnopqrstuvwxyz123456")) {
			t.Fatal("npm token remained in exported evidence")
		}
	}
	output := filepath.Join(base, "output")
	if err := Write(output, result); err != nil {
		t.Fatal(err)
	}
	for _, kind := range result.Envelope.AgentKinds {
		if _, err := os.Stat(filepath.Join(output, "context", kind)); err != nil {
			t.Fatalf("missing %s evidence: %v", kind, err)
		}
	}
}

func equalStrings(left, right []string) bool {
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	return len(left) == len(right) && stringsJoin(left) == stringsJoin(right)
}

func stringsJoin(values []string) string {
	var result bytes.Buffer
	for _, value := range values {
		result.WriteString(value)
		result.WriteByte(0)
	}
	return result.String()
}
