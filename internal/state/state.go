package state

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jbaehova/onthego/internal/config"
	"github.com/jbaehova/onthego/internal/otgerror"
)

type TransferStage string

const (
	Preparing TransferStage = "PREPARING"
	Captured  TransferStage = "CAPTURED"
	Uploaded  TransferStage = "UPLOADED"
	Verified  TransferStage = "VERIFIED"
	Restored  TransferStage = "RESTORED"
	Started   TransferStage = "STARTED"
	Acked     TransferStage = "ACKED"
	Failed    TransferStage = "FAILED"
	Cancelled TransferStage = "CANCELLED"
)

type AgentState string

const (
	AgentNotStarted AgentState = "NOT_STARTED"
	AgentRunning    AgentState = "RUNNING"
	AgentSucceeded  AgentState = "SUCCEEDED"
	AgentFailed     AgentState = "FAILED"
	AgentStopped    AgentState = "STOPPED"
)

type Snapshot struct {
	ID        string    `json:"id"`
	Parents   []string  `json:"parents,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Package   string    `json:"package,omitempty"`
}

type Environment struct {
	ID               string     `json:"id"`
	LatestSnapshotID string     `json:"latest_snapshot_id,omitempty"`
	Dirty            bool       `json:"dirty"`
	ObservedAt       time.Time  `json:"observed_at,omitempty"`
	RunState         AgentState `json:"run_state,omitempty"`
	SandboxID        string     `json:"sandbox_id,omitempty"`
	SessionID        string     `json:"session_id,omitempty"`
	CommandID        string     `json:"command_id,omitempty"`
}

type Transfer struct {
	ID             string        `json:"id"`
	Target         string        `json:"target"`
	PayloadSHA256  string        `json:"payload_sha256,omitempty"`
	SnapshotID     string        `json:"snapshot_id,omitempty"`
	Stage          TransferStage `json:"stage"`
	LastSuccessful TransferStage `json:"last_successful,omitempty"`
	Retryable      bool          `json:"retryable"`
	Error          string        `json:"error,omitempty"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

type StatusSummary struct {
	Text            string    `json:"text,omitempty"`
	Model           string    `json:"model,omitempty"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	GeneratedAt     time.Time `json:"generated_at,omitempty"`
}

type ProjectState struct {
	SchemaVersion       int                    `json:"schema_version"`
	ProjectID           string                 `json:"project_id"`
	Generation          uint64                 `json:"generation"`
	ActiveEnvironmentID string                 `json:"active_environment_id"`
	Snapshots           map[string]Snapshot    `json:"snapshots"`
	Environments        map[string]Environment `json:"environments"`
	Transfers           map[string]Transfer    `json:"transfers"`
	LatestSummary       StatusSummary          `json:"latest_summary,omitempty"`
	RecentLogExcerpt    string                 `json:"recent_log_excerpt,omitempty"`
	RecentRuns          json.RawMessage        `json:"recent_runs,omitempty"`
}

type Event struct {
	At         time.Time     `json:"at"`
	TransferID string        `json:"transfer_id,omitempty"`
	RunID      string        `json:"run_id,omitempty"`
	Stage      TransferStage `json:"stage,omitempty"`
	Message    string        `json:"message"`
	DurationMS int64         `json:"duration_ms,omitempty"`
}

func Default(projectID string) ProjectState {
	return ProjectState{
		SchemaVersion:       1,
		ProjectID:           projectID,
		ActiveEnvironmentID: "local",
		Snapshots:           map[string]Snapshot{},
		Environments:        map[string]Environment{"local": {ID: "local"}},
		Transfers:           map[string]Transfer{},
	}
}

func Load(projectID string) (ProjectState, error) {
	dir, err := config.ProjectDataRoot(projectID)
	if err != nil {
		return ProjectState{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if os.IsNotExist(err) {
		return Default(projectID), nil
	}
	if err != nil {
		return ProjectState{}, err
	}
	var st ProjectState
	if err := json.Unmarshal(data, &st); err != nil {
		return ProjectState{}, err
	}
	if st.ProjectID != projectID || st.SchemaVersion != 1 {
		return ProjectState{}, errors.New("state belongs to another project or schema")
	}
	return st, nil
}

func Save(st *ProjectState, expected uint64) error {
	if st.Generation != expected {
		return &otgerror.Error{Code: otgerror.CodeGenerationConflict, Message: fmt.Sprintf("expected generation %d, found %d", expected, st.Generation)}
	}
	dir, err := config.ProjectDataRoot(st.ProjectID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st.Generation++
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "state-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, "state.json"))
}

func AppendEvent(projectID string, event Event) error {
	dir, err := config.ProjectDataRoot(projectID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	w := bufio.NewWriter(f)
	if err := json.NewEncoder(w).Encode(event); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Sync()
}

func Relation(st ProjectState, leftID, rightID string) string {
	left, lok := st.Environments[leftID]
	right, rok := st.Environments[rightID]
	if !lok || !rok || left.LatestSnapshotID == "" || right.LatestSnapshotID == "" {
		return "unknown"
	}
	if left.LatestSnapshotID == right.LatestSnapshotID {
		return "synced"
	}
	if ancestor(st, left.LatestSnapshotID, right.LatestSnapshotID) {
		return "remote_ahead"
	}
	if ancestor(st, right.LatestSnapshotID, left.LatestSnapshotID) {
		return "local_ahead"
	}
	if commonAncestor(st, left.LatestSnapshotID, right.LatestSnapshotID) {
		return "diverged"
	}
	return "unrelated"
}

func ancestor(st ProjectState, ancestorID, childID string) bool {
	seen := map[string]bool{}
	queue := []string{childID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id == ancestorID {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		queue = append(queue, st.Snapshots[id].Parents...)
	}
	return false
}

func commonAncestor(st ProjectState, a, b string) bool {
	for id := range st.Snapshots {
		if ancestor(st, id, a) && ancestor(st, id, b) {
			return true
		}
	}
	return false
}
