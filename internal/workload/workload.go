package workload

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jbaehova/onthego/internal/config"
)

const SchemaVersion = 1

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

type Definition struct {
	SchemaVersion      int                 `json:"schema_version"`
	Name               string              `json:"name"`
	Kind               string              `json:"kind"`
	Image              string              `json:"image"`
	ImageDigest        string              `json:"image_digest,omitempty"`
	GPUCount           int                 `json:"gpu_count"`
	GPUTypes           []string            `json:"gpu_types,omitempty"`
	Argv               []string            `json:"argv"`
	Workdir            string              `json:"workdir"`
	ModelRef           string              `json:"model_ref,omitempty"`
	ModelCommit        string              `json:"model_commit,omitempty"`
	DatasetRef         string              `json:"dataset_ref,omitempty"`
	DatasetCommit      string              `json:"dataset_commit,omitempty"`
	OutputPath         string              `json:"output_path,omitempty"`
	CheckpointPath     string              `json:"checkpoint_path,omitempty"`
	HealthPath         string              `json:"health_path,omitempty"`
	Port               int                 `json:"port,omitempty"`
	TimeoutSeconds     int                 `json:"timeout_seconds"`
	SecretMappings     map[string]string   `json:"secret_mappings,omitempty"`
	SecretAllowedHosts map[string][]string `json:"secret_allowed_hosts,omitempty"`
	ContextEnvelopeID  string              `json:"context_envelope_id"`
}

type Request struct {
	SchemaVersion      int        `json:"schema_version"`
	ProjectID          string     `json:"project_id"`
	TransferID         string     `json:"transfer_id"`
	ExpectedGeneration uint64     `json:"expected_generation"`
	Definition         Definition `json:"definition"`
	CreatedAt          time.Time  `json:"created_at"`
}

type Receipt struct {
	SchemaVersion  int               `json:"schema_version"`
	ProjectID      string            `json:"project_id"`
	TransferID     string            `json:"transfer_id"`
	WorkloadName   string            `json:"workload_name"`
	Kind           string            `json:"kind"`
	SandboxID      string            `json:"sandbox_id"`
	SessionID      string            `json:"session_id"`
	CommandID      string            `json:"command_id"`
	PID            int               `json:"pid,omitempty"`
	RequestSHA256  string            `json:"request_sha256"`
	RunState       string            `json:"run_state"`
	ImageDigest    string            `json:"image_digest,omitempty"`
	GPUType        string            `json:"gpu_type,omitempty"`
	ModelCommit    string            `json:"model_commit,omitempty"`
	DatasetCommit  string            `json:"dataset_commit,omitempty"`
	OutputPath     string            `json:"output_path,omitempty"`
	CheckpointPath string            `json:"checkpoint_path,omitempty"`
	Endpoint       string            `json:"endpoint,omitempty"`
	ServedModel    string            `json:"served_model,omitempty"`
	Health         string            `json:"health,omitempty"`
	ArtifactSHA256 map[string]string `json:"artifact_sha256,omitempty"`
	HubCommit      string            `json:"hub_commit,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	ObservedAt     time.Time         `json:"observed_at"`
}

func FromConfig(name string, value config.Workload, envelopeID string) Definition {
	return Definition{
		SchemaVersion:     SchemaVersion,
		Name:              name,
		Kind:              value.Kind,
		Image:             value.Image,
		ImageDigest:       value.ImageDigest,
		GPUCount:          value.GPUCount,
		GPUTypes:          append([]string(nil), value.GPUTypes...),
		Argv:              append([]string(nil), value.Argv...),
		Workdir:           value.Workdir,
		ModelRef:          value.ModelRef,
		DatasetRef:        value.DatasetRef,
		OutputPath:        value.OutputPath,
		CheckpointPath:    value.CheckpointPath,
		HealthPath:        value.HealthPath,
		Port:              value.Port,
		TimeoutSeconds:    value.TimeoutSeconds,
		SecretMappings:    cloneMap(value.SecretMappings),
		ContextEnvelopeID: envelopeID,
	}
}

func NewRequest(projectID string, generation uint64, definition Definition) Request {
	return Request{
		SchemaVersion:      SchemaVersion,
		ProjectID:          projectID,
		TransferID:         uuid.NewString(),
		ExpectedGeneration: generation,
		Definition:         definition,
		CreatedAt:          time.Now().UTC(),
	}
}

func (d *Definition) Validate() error {
	if d.SchemaVersion != SchemaVersion || !safeName.MatchString(d.Name) {
		return errors.New("invalid workload schema or name")
	}
	if d.Kind != "agent" && d.Kind != "fine_tune" && d.Kind != "serve" {
		return errors.New("workload kind must be agent, fine_tune, or serve")
	}
	if d.Image == "" || len(d.Argv) == 0 || d.Argv[0] == "" {
		return errors.New("workload image and argv are required")
	}
	if d.GPUCount < 0 || (d.Kind != "agent" && d.GPUCount < 1) {
		return errors.New("fine_tune and serve workloads must request at least one GPU")
	}
	if d.TimeoutSeconds <= 0 {
		d.TimeoutSeconds = 3600
	}
	if d.Workdir == "" {
		d.Workdir = "/workspace"
	}
	if !filepath.IsAbs(d.Workdir) || strings.Contains(filepath.Clean(d.Workdir), "..") {
		return errors.New("workdir must be an absolute normalized sandbox path")
	}
	for env, secret := range d.SecretMappings {
		if !safeName.MatchString(env) || !safeName.MatchString(secret) {
			return fmt.Errorf("invalid secret mapping %q", env)
		}
	}
	if d.Kind == "serve" && (d.Port < 1 || d.Port > 65535 || d.HealthPath == "") {
		return errors.New("serve workload requires a port and health path")
	}
	return nil
}

func (r Request) CanonicalJSON() ([]byte, error) {
	return json.Marshal(r)
}

func (r Request) SHA256() (string, error) {
	data, err := r.CanonicalJSON()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func RedactedDefinition(d Definition) Definition {
	copy := d
	copy.SecretMappings = cloneMap(d.SecretMappings)
	copy.SecretAllowedHosts = map[string][]string{}
	keys := make([]string, 0, len(copy.SecretMappings))
	for key := range copy.SecretMappings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return copy
}

func cloneMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
