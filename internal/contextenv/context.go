package contextenv

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jbaehova/onthego/internal/handoff"
	"github.com/jbaehova/onthego/internal/otgerror"
)

const (
	SchemaVersion = 1
	MaxLogBytes   = 8 << 20
)

type Session struct {
	AgentKind string    `json:"agent_kind"`
	ID        string    `json:"id"`
	Path      string    `json:"path,omitempty"`
	CWD       string    `json:"cwd"`
	UpdatedAt time.Time `json:"updated_at"`
	SizeBytes int64     `json:"size_bytes"`
	Data      []byte    `json:"-"`
}

type Evidence struct {
	RelativePath string `json:"relative_path"`
	SourceHash   string `json:"source_path_sha256"`
	StartOffset  int64  `json:"start_offset"`
	EndOffset    int64  `json:"end_offset"`
	SHA256       string `json:"sha256"`
}

type AgentSource struct {
	AgentKind   string    `json:"agent_kind"`
	SessionID   string    `json:"session_id"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
	Evidence    string    `json:"evidence"`
	SizeBytes   int64     `json:"size_bytes"`
	StartOffset int64     `json:"start_offset"`
	Truncated   bool      `json:"truncated"`
}

type Redaction struct {
	Rule  string `json:"rule"`
	Count int    `json:"count"`
}

type Envelope struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	ProjectID     string            `json:"project_id"`
	SourceCWDHash string            `json:"source_cwd_sha256"`
	AgentKind     string            `json:"agent_kind"`
	AgentKinds    []string          `json:"agent_kinds,omitempty"`
	AgentVersion  string            `json:"agent_version,omitempty"`
	SessionID     string            `json:"session_id"`
	Sessions      []AgentSource     `json:"sessions,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	Evidence      []Evidence        `json:"evidence"`
	Redactions    []Redaction       `json:"redactions,omitempty"`
	Handoff       handoff.Input     `json:"handoff"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type ExportResult struct {
	Envelope Envelope          `json:"envelope"`
	Log      []byte            `json:"-"`
	Logs     map[string][]byte `json:"-"`
	Handoff  []byte            `json:"-"`
}

type Roots struct {
	CodexSessions  string
	CursorProjects string
	ClaudeProjects string
	HermesHome     string
}

var redactionRules = []struct {
	name string
	re   *regexp.Regexp
}{
	{"opaque_agent_payload", regexp.MustCompile(`(?i)"encrypted_content"\s*:\s*"[^"]*"`)},
	{"bearer", regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._~+/=-]{12,}`)},
	{"private_key", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)},
	{"assignment", regexp.MustCompile(`(?im)(API[_-]?KEY|[A-Z0-9_]*TOKEN|[A-Z0-9_]*SECRET|[A-Z0-9_]*PASSWORD|HF_TOKEN|CODEX_API_KEY|NPM_KEY)\s*[:=]\s*["']?[^\s,"']{6,}`)},
	{"common_token", regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{16,}|hf_[A-Za-z0-9]{16,}|gh[pousr]_[A-Za-z0-9]{16,}|npm_[A-Za-z0-9]{20,})\b`)},
}

func DiscoverAll(root string) ([]Session, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return DiscoverAllAt(root, Roots{
		CodexSessions:  filepath.Join(home, ".codex", "sessions"),
		CursorProjects: filepath.Join(home, ".cursor", "projects"),
		ClaudeProjects: filepath.Join(home, ".claude", "projects"),
		HermesHome:     filepath.Join(home, ".hermes"),
	})
}

func DiscoverAllAt(root string, roots Roots) ([]Session, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var sessions []Session
	collect := func(source string, values []Session, discoverErr error) error {
		if discoverErr != nil {
			return fmt.Errorf("discover %s sessions: %w", source, discoverErr)
		}
		sessions = append(sessions, values...)
		return nil
	}
	codex, codexErr := discoverFiles(absRoot, roots.CodexSessions, "codex", false, map[string]bool{".jsonl": true})
	if err := collect("codex", codex, codexErr); err != nil {
		return nil, err
	}
	for _, candidate := range projectDirectories(roots.CursorProjects, absRoot, false) {
		values, discoverErr := discoverFiles(absRoot, filepath.Join(candidate, "agent-transcripts"), "cursor", true, map[string]bool{".jsonl": true, ".txt": true})
		if err := collect("cursor", values, discoverErr); err != nil {
			return nil, err
		}
	}
	for _, candidate := range projectDirectories(roots.ClaudeProjects, absRoot, true) {
		values, discoverErr := discoverFiles(absRoot, candidate, "claude", true, map[string]bool{".jsonl": true})
		if err := collect("claude", values, discoverErr); err != nil {
			return nil, err
		}
	}
	hermes, hermesErr := discoverHermes(absRoot, roots.HermesHome)
	if err := collect("hermes", hermes, hermesErr); err != nil {
		return nil, err
	}

	deduplicated := make(map[string]Session, len(sessions))
	for _, session := range sessions {
		key := session.AgentKind + "\x00" + session.ID + "\x00" + session.Path
		if existing, ok := deduplicated[key]; !ok || session.UpdatedAt.After(existing.UpdatedAt) {
			deduplicated[key] = session
		}
	}
	sessions = sessions[:0]
	for _, session := range deduplicated {
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].AgentKind != sessions[j].AgentKind {
			return sessions[i].AgentKind < sessions[j].AgentKind
		}
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	return sessions, nil
}

func DiscoverCodex(root string) ([]Session, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return DiscoverCodexAt(root, filepath.Join(home, ".codex", "sessions"))
}

func DiscoverCodexAt(root, sessionsRoot string) ([]Session, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return discoverFiles(absRoot, sessionsRoot, "codex", false, map[string]bool{".jsonl": true})
}

func projectDirectories(base, root string, leadingDash bool) []string {
	if base == "" {
		return nil
	}
	slug := strings.ReplaceAll(strings.TrimPrefix(filepath.Clean(root), string(filepath.Separator)), string(filepath.Separator), "-")
	values := []string{filepath.Join(base, slug)}
	if leadingDash {
		values = append(values, filepath.Join(base, "-"+slug))
	}
	return values
}

func discoverFiles(root, sessionsRoot, agentKind string, trustDirectory bool, extensions map[string]bool) ([]Session, error) {
	if sessionsRoot == "" {
		return nil, nil
	}
	var sessions []Session
	err := filepath.WalkDir(sessionsRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) || os.IsPermission(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || !extensions[strings.ToLower(filepath.Ext(entry.Name()))] {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() == 0 {
			return nil
		}
		session, ok, err := inspectSession(path, root, agentKind, trustDirectory)
		if err != nil {
			return err
		}
		if ok {
			session.UpdatedAt = info.ModTime().UTC()
			session.SizeBytes = info.Size()
			sessions = append(sessions, session)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt) })
	return sessions, nil
}

func inspectSession(path, root, agentKind string, trustDirectory bool) (Session, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return Session{}, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		return Session{}, false, err
	}
	var session Session
	session.AgentKind = agentKind
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		var value map[string]any
		if json.Unmarshal(scanner.Bytes(), &value) != nil {
			continue
		}
		populateSession(&session, value)
		if session.CWD != "" && session.ID != "" {
			break
		}
	}
	if session.CWD == "" {
		var value map[string]any
		if json.Unmarshal(data, &value) == nil {
			populateSession(&session, value)
		}
	}
	if session.CWD == "" && trustDirectory {
		session.CWD = root
	}
	if !pathBelongsToRoot(session.CWD, root) {
		return Session{}, false, nil
	}
	if session.ID == "" {
		session.ID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	session.Path = path
	return session, true, nil
}

func populateSession(session *Session, value map[string]any) {
	if session.CWD == "" {
		session.CWD = firstString(value, "cwd", "working_directory", "workdir", "workspace", "workspace_path", "project_path")
	}
	if session.ID == "" {
		session.ID = firstString(value, "session_id", "sessionId", "conversation_id", "id")
	}
}

func discoverHermes(root, home string) ([]Session, error) {
	if home == "" {
		return nil, nil
	}
	legacy, _ := discoverFiles(root, filepath.Join(home, "sessions"), "hermes", false, map[string]bool{".jsonl": true, ".json": true})
	if _, err := os.Stat(filepath.Join(home, "state.db")); err != nil {
		return legacy, nil
	}
	hermesBinary, err := exec.LookPath("hermes")
	if err != nil {
		return legacy, nil
	}
	temp, err := os.CreateTemp("", "onthego-hermes-*.jsonl")
	if err != nil {
		return legacy, nil
	}
	exportPath := temp.Name()
	_ = temp.Close()
	defer os.Remove(exportPath)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, hermesBinary, "sessions", "export", exportPath, "--redact")
	command.Env = os.Environ()
	if err := command.Run(); err != nil {
		return legacy, nil
	}
	file, err := os.Open(exportPath)
	if err != nil {
		return legacy, nil
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, 128<<20))
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var value map[string]any
		if json.Unmarshal(line, &value) != nil {
			continue
		}
		cwd := firstString(value, "cwd", "working_directory", "workspace", "workspace_path", "project_path")
		if !pathBelongsToRoot(cwd, root) {
			continue
		}
		id := firstString(value, "session_id", "sessionId", "id")
		if id == "" {
			continue
		}
		legacy = append(legacy, Session{AgentKind: "hermes", ID: id, Path: "hermes://" + id, CWD: cwd, SizeBytes: int64(len(line)), Data: append(line, '\n')})
	}
	return legacy, nil
}

func pathBelongsToRoot(path, root string) bool {
	if path == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(abs))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func firstString(value any, keys ...string) string {
	keySet := map[string]bool{}
	for _, key := range keys {
		keySet[key] = true
	}
	var search func(any) string
	search = func(current any) string {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				if keySet[key] {
					if text, ok := child.(string); ok {
						return text
					}
				}
			}
			for _, child := range typed {
				if result := search(child); result != "" {
					return result
				}
			}
		case []any:
			for _, child := range typed {
				if result := search(child); result != "" {
					return result
				}
			}
		}
		return ""
	}
	return search(value)
}

func Kinds(sessions []Session) []string {
	seen := map[string]bool{}
	var result []string
	for _, session := range sessions {
		if session.AgentKind != "" && !seen[session.AgentKind] {
			seen[session.AgentKind] = true
			result = append(result, session.AgentKind)
		}
	}
	sort.Strings(result)
	return result
}

func Select(sessions []Session, id string) (Session, error) {
	if id != "" {
		for _, session := range sessions {
			if session.ID == id {
				return session, nil
			}
		}
		return Session{}, otgerror.New(otgerror.CodeSessionFormat, "selected session does not belong to this project")
	}
	if len(sessions) == 0 {
		return Session{}, otgerror.New(otgerror.CodeSessionFormat, "no supported agent session belongs to this project")
	}
	return sessions[0], nil
}

func Export(projectID, root, agentVersion string, session Session, input handoff.Input) (ExportResult, error) {
	if session.AgentKind == "" {
		session.AgentKind = "codex"
	}
	return ExportAll(projectID, root, agentVersion, []Session{session}, input)
}

func ExportAll(projectID, root, agentVersion string, sessions []Session, input handoff.Input) (ExportResult, error) {
	if len(sessions) == 0 {
		return ExportResult{}, otgerror.New(otgerror.CodeSessionFormat, "no Codex, Cursor, Hermes, or Claude session belongs to this project")
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].AgentKind != sessions[j].AgentKind {
			return sessions[i].AgentKind < sessions[j].AgentKind
		}
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	cwdHash := sha256.Sum256([]byte(filepath.Clean(root)))
	result := ExportResult{Logs: map[string][]byte{}}
	result.Envelope = Envelope{
		SchemaVersion: SchemaVersion,
		ID:            uuid.NewString(),
		ProjectID:     projectID,
		SourceCWDHash: hex.EncodeToString(cwdHash[:]),
		AgentKind:     "multi-agent",
		AgentKinds:    Kinds(sessions),
		AgentVersion:  agentVersion,
		SessionID:     "all",
		CreatedAt:     time.Now().UTC(),
		Handoff:       input,
		Metadata:      map[string]string{"discovery": "automatic", "session_count": fmt.Sprintf("%d", len(sessions))},
	}
	redactionCounts := map[string]int{}
	for _, session := range sessions {
		data := session.Data
		if data == nil {
			var err error
			data, err = os.ReadFile(session.Path)
			if err != nil {
				return ExportResult{}, err
			}
		}
		originalSize := int64(len(data))
		startOffset := int64(0)
		truncated := false
		if len(data) > MaxLogBytes {
			startOffset = int64(len(data) - MaxLogBytes)
			data = data[len(data)-MaxLogBytes:]
			truncated = true
		}
		redacted, report := Redact(data)
		for _, item := range report {
			redactionCounts[item.Rule] += item.Count
		}
		pathHash := sha256.Sum256([]byte(filepath.Clean(session.Path)))
		contentHash := sha256.Sum256(redacted)
		extension := strings.ToLower(filepath.Ext(session.Path))
		if extension != ".txt" {
			extension = ".jsonl"
		}
		filename := safeName(session.ID) + "-" + hex.EncodeToString(pathHash[:4]) + extension
		relative := filepath.ToSlash(filepath.Join("context", safeName(session.AgentKind), filename))
		result.Logs[relative] = redacted
		result.Envelope.Evidence = append(result.Envelope.Evidence, Evidence{RelativePath: relative, SourceHash: hex.EncodeToString(pathHash[:]), StartOffset: startOffset, EndOffset: originalSize, SHA256: hex.EncodeToString(contentHash[:])})
		result.Envelope.Sessions = append(result.Envelope.Sessions, AgentSource{AgentKind: session.AgentKind, SessionID: session.ID, UpdatedAt: session.UpdatedAt, Evidence: relative, SizeBytes: originalSize, StartOffset: startOffset, Truncated: truncated})
		result.Envelope.Handoff.EvidencePaths = append(result.Envelope.Handoff.EvidencePaths, relative)
	}
	for name, count := range redactionCounts {
		result.Envelope.Redactions = append(result.Envelope.Redactions, Redaction{Rule: name, Count: count})
	}
	sort.Slice(result.Envelope.Redactions, func(i, j int) bool { return result.Envelope.Redactions[i].Rule < result.Envelope.Redactions[j].Rule })
	result.Envelope.Handoff.EvidencePaths = uniqueSorted(result.Envelope.Handoff.EvidencePaths)
	result.Handoff = handoff.Render(result.Envelope.Handoff)
	if len(result.Logs) == 1 {
		for _, data := range result.Logs {
			result.Log = data
		}
	}
	return result, nil
}

func safeName(value string) string {
	value = regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(value, "-")
	value = strings.Trim(value, ".-")
	if value == "" {
		return "session"
	}
	if len(value) > 96 {
		return value[:96]
	}
	return value
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func Redact(data []byte) ([]byte, []Redaction) {
	redacted := append([]byte(nil), data...)
	var report []Redaction
	for _, rule := range redactionRules {
		count := len(rule.re.FindAll(redacted, -1))
		if count == 0 {
			continue
		}
		redacted = rule.re.ReplaceAll(redacted, []byte("[REDACTED:"+rule.name+"]"))
		report = append(report, Redaction{Rule: rule.name, Count: count})
	}
	return redacted, report
}

func Write(dir string, result ExportResult) error {
	if dir == "" {
		return errors.New("context output directory is required")
	}
	if err := os.MkdirAll(filepath.Join(dir, "context"), 0o700); err != nil {
		return err
	}
	if len(result.Logs) == 0 && result.Log != nil {
		result.Logs = map[string][]byte{"context/codex/session.jsonl": result.Log}
	}
	for relative, data := range result.Logs {
		clean := filepath.Clean(relative)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || !strings.HasPrefix(filepath.ToSlash(clean), "context/") {
			return errors.New("context evidence path escapes the envelope")
		}
		path := filepath.Join(dir, clean)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "HANDOFF.md"), result.Handoff, 0o600); err != nil {
		return err
	}
	manifest, err := json.MarshalIndent(result.Envelope, "", "  ")
	if err != nil {
		return err
	}
	if bytes.Contains(manifest, []byte("auth.json")) {
		return fmt.Errorf("context envelope contains a forbidden auth path")
	}
	return os.WriteFile(filepath.Join(dir, "context-envelope.json"), manifest, 0o600)
}
