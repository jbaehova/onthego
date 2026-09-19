package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

const FileName = ".onthego.yaml"

var gitignoreEntries = []string{
	"AGENTS.md",
	".agents/",
	".env",
	".env.*",
	"*.env",
	".onthego.local/",
	"*.otg",
	"HANDOFF.md",
}

type Target struct {
	Provider      string `yaml:"provider" json:"provider"`
	SandboxID     string `yaml:"sandbox_id,omitempty" json:"sandbox_id,omitempty"`
	Image         string `yaml:"image,omitempty" json:"image,omitempty"`
	ReceiverPath  string `yaml:"receiver_path,omitempty" json:"receiver_path,omitempty"`
	AgeRecipient  string `yaml:"age_recipient,omitempty" json:"age_recipient,omitempty"`
	SigningPublic string `yaml:"signing_public,omitempty" json:"signing_public,omitempty"`
}

type Summary struct {
	Kind         string `yaml:"kind" json:"kind"`
	Endpoint     string `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	Model        string `yaml:"model,omitempty" json:"model,omitempty"`
	DeploymentID string `yaml:"deployment_id,omitempty" json:"deployment_id,omitempty"`
}

type DNSimple struct {
	Account string `yaml:"account,omitempty" json:"account,omitempty"`
	Zone    string `yaml:"zone,omitempty" json:"zone,omitempty"`
	BaseURL string `yaml:"base_url,omitempty" json:"base_url,omitempty"`
}

type SecretBinding struct {
	Name  string   `yaml:"name" json:"name"`
	Hosts []string `yaml:"hosts,omitempty" json:"hosts,omitempty"`
}

type Workload struct {
	Kind             string            `yaml:"kind" json:"kind"`
	Image            string            `yaml:"image" json:"image"`
	ImageDigest      string            `yaml:"image_digest,omitempty" json:"image_digest,omitempty"`
	GPUCount         int               `yaml:"gpu_count" json:"gpu_count"`
	GPUTypes         []string          `yaml:"gpu_types,omitempty" json:"gpu_types,omitempty"`
	Argv             []string          `yaml:"argv" json:"argv"`
	Workdir          string            `yaml:"workdir,omitempty" json:"workdir,omitempty"`
	ModelRef         string            `yaml:"model_ref,omitempty" json:"model_ref,omitempty"`
	DatasetRef       string            `yaml:"dataset_ref,omitempty" json:"dataset_ref,omitempty"`
	OutputPath       string            `yaml:"output_path,omitempty" json:"output_path,omitempty"`
	CheckpointPath   string            `yaml:"checkpoint_path,omitempty" json:"checkpoint_path,omitempty"`
	HealthPath       string            `yaml:"health_path,omitempty" json:"health_path,omitempty"`
	Port             int               `yaml:"port,omitempty" json:"port,omitempty"`
	TimeoutSeconds   int               `yaml:"timeout_seconds,omitempty" json:"timeout_seconds,omitempty"`
	SecretMappings   map[string]string `yaml:"secret_mappings,omitempty" json:"secret_mappings,omitempty"`
	PublishAllowlist []string          `yaml:"publish_allowlist,omitempty" json:"publish_allowlist,omitempty"`
}

type Config struct {
	SchemaVersion int                      `yaml:"schema_version" json:"schema_version"`
	ProjectID     string                   `yaml:"project_id" json:"project_id"`
	Include       []string                 `yaml:"include,omitempty" json:"include,omitempty"`
	Exclude       []string                 `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	Agent         string                   `yaml:"agent" json:"agent"`
	Summary       Summary                  `yaml:"summary" json:"summary"`
	Targets       map[string]Target        `yaml:"targets,omitempty" json:"targets,omitempty"`
	Secrets       map[string]SecretBinding `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Workloads     map[string]Workload      `yaml:"workloads,omitempty" json:"workloads,omitempty"`
	DNSimple      DNSimple                 `yaml:"dnsimple,omitempty" json:"dnsimple,omitempty"`
}

func Default() Config {
	return Config{
		SchemaVersion: 1,
		ProjectID:     uuid.NewString(),
		Include:       []string{FileName},
		Exclude: []string{
			".git", "node_modules", "vendor", "dist", "build", ".cache", "__pycache__",
		},
		Agent:   "auto",
		Summary: Summary{Kind: "local"},
		Targets: map[string]Target{
			"daytona": {Provider: "daytona", ReceiverPath: "/usr/local/bin/onthego"},
		},
		Secrets: map[string]SecretBinding{},
		Workloads: map[string]Workload{
			"agent-sync": {
				Kind:           "agent",
				Image:          "debian:bookworm-slim",
				GPUCount:       0,
				Argv:           []string{"/bin/sh", "-lc", "cp ../context/HANDOFF.md ./HANDOFF.md"},
				Workdir:        "/workspace",
				OutputPath:     "HANDOFF.md",
				TimeoutSeconds: 600,
			},
		},
	}
}

func FindRoot(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", errors.New("not inside a Git repository")
		}
		abs = parent
	}
}

func Load(root string) (Config, error) {
	data, err := os.ReadFile(filepath.Join(root, FileName))
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", FileName, err)
	}
	if cfg.SchemaVersion != 1 || cfg.ProjectID == "" {
		return Config{}, fmt.Errorf("unsupported or incomplete %s", FileName)
	}
	if cfg.Targets == nil {
		cfg.Targets = map[string]Target{}
	}
	if cfg.Secrets == nil {
		cfg.Secrets = map[string]SecretBinding{}
	}
	if cfg.Workloads == nil {
		cfg.Workloads = map[string]Workload{}
	}
	return cfg, nil
}

func Save(root string, cfg Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	path := filepath.Join(root, FileName)
	tmp, err := os.CreateTemp(root, ".onthego.yaml-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
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
	return os.Rename(tmpName, path)
}

func EnsureGitignore(root string) (bool, error) {
	path := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("refusing to modify a symlinked .gitignore")
	}
	existing := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		existing[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, entry := range gitignoreEntries {
		if !existing[entry] {
			missing = append(missing, entry)
		}
	}
	if len(missing) == 0 {
		return false, nil
	}
	updated := append([]byte(nil), data...)
	if len(updated) > 0 && updated[len(updated)-1] != '\n' {
		updated = append(updated, '\n')
	}
	if len(updated) > 0 {
		updated = append(updated, '\n')
	}
	updated = append(updated, []byte("# ONTHEGO local files\n")...)
	updated = append(updated, []byte(strings.Join(missing, "\n")+"\n")...)
	tmp, err := os.CreateTemp(root, ".gitignore-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return false, err
	}
	if _, err := tmp.Write(updated); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, err
	}
	return true, nil
}

func ValidateRelative(root, raw string) (string, error) {
	if raw == "" || filepath.IsAbs(raw) {
		return "", fmt.Errorf("path must be project-relative: %q", raw)
	}
	clean := filepath.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes project root: %q", raw)
	}
	abs, err := filepath.Abs(filepath.Join(root, clean))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes project root: %q", raw)
	}
	return filepath.ToSlash(rel), nil
}

func DataRoot() (string, error) {
	if override := os.Getenv("ONTHEGO_DATA_HOME"); override != "" {
		return filepath.Abs(override)
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "onthego"), nil
}

func ProjectDataRoot(projectID string) (string, error) {
	base, err := DataRoot()
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(projectID, `/\\`) || projectID == "" {
		return "", errors.New("invalid project ID")
	}
	return filepath.Join(base, "projects", projectID), nil
}
