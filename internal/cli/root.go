package cli

import (
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
	"strings"
	"time"

	"github.com/jbaehova/onthego/internal/capture"
	"github.com/jbaehova/onthego/internal/config"
	"github.com/jbaehova/onthego/internal/contextenv"
	"github.com/jbaehova/onthego/internal/controlplane"
	"github.com/jbaehova/onthego/internal/envfile"
	"github.com/jbaehova/onthego/internal/gitx"
	"github.com/jbaehova/onthego/internal/handoff"
	"github.com/jbaehova/onthego/internal/identity"
	"github.com/jbaehova/onthego/internal/model/huggingface"
	"github.com/jbaehova/onthego/internal/otgerror"
	"github.com/jbaehova/onthego/internal/receiver"
	"github.com/jbaehova/onthego/internal/restore"
	"github.com/jbaehova/onthego/internal/state"
	openaisummary "github.com/jbaehova/onthego/internal/summary/openai"
	daytonatransport "github.com/jbaehova/onthego/internal/transport/daytona"
	"github.com/jbaehova/onthego/internal/workload"
	"github.com/spf13/cobra"
)

type app struct {
	version string
	commit  string
	builtAt string
	json    bool
}

func Execute(version, commit, builtAt string) error {
	if _, err := envfile.Load("."); err != nil {
		return otgerror.Wrap(otgerror.CodePrecondition, "load root .env", err)
	}
	application := &app{version: version, commit: commit, builtAt: builtAt}
	root := application.rootCommand()
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	return root.Execute()
}

func ExitCode(err error) int {
	var typed *otgerror.Error
	if !errors.As(err, &typed) {
		return 1
	}
	switch typed.Code {
	case otgerror.CodeInput:
		return 2
	case otgerror.CodePrecondition, otgerror.CodeUnsupportedGit, otgerror.CodeSecretPolicy, otgerror.CodeSessionFormat:
		return 3
	case otgerror.CodeTargetAuth, otgerror.CodeTargetUnavailable:
		return 4
	case otgerror.CodePackageInvalid, otgerror.CodeRestoreFailed:
		return 5
	case otgerror.CodeGenerationConflict, otgerror.CodeRunUncertain, otgerror.CodeSourceChanged:
		return 6
	default:
		return 1
	}
}

func (a *app) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "onthego",
		Short:         "Carry agent context and work between local and Daytona environments",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetHelpCommand(&cobra.Command{Use: "help", Hidden: true})
	root.SetUsageTemplate(`Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if .HasAvailableSubCommands}}

Available Commands:{{range .Commands}}{{if .IsAvailableCommand}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`)
	root.PersistentFlags().BoolVar(&a.json, "json", false, "write schema-versioned JSON to stdout")
	root.AddCommand(a.initCommand(), a.loginCommand(), a.passCommand(), a.pullCommand(), a.statusCommand())
	advanced := []*cobra.Command{
		a.versionCommand(),
		a.doctorCommand(),
		a.inspectCommand(),
		a.targetCommand(),
		a.contextCommand(),
		a.snapshotCommand(),
		a.workloadCommand(),
		a.modelCommand(),
		a.receiveCommand(),
	}
	for _, command := range advanced {
		command.Hidden = true
		root.AddCommand(command)
	}
	return root
}

func (a *app) loginCommand() *cobra.Command {
	var serverURL string
	command := &cobra.Command{
		Use:   "login",
		Args:  cobra.NoArgs,
		Short: "Log in to the ONTHEGO control plane",
		RunE: func(cmd *cobra.Command, args []string) error {
			if serverURL == "" {
				serverURL = os.Getenv("ONTHEGO_CONTROL_PLANE_URL")
			}
			bootstrap := os.Getenv("ONTHEGO_LOGIN_BOOTSTRAP_TOKEN")
			if serverURL == "" || bootstrap == "" {
				return otgerror.New(otgerror.CodePrecondition, "ONTHEGO_CONTROL_PLANE_URL and ONTHEGO_LOGIN_BOOTSTRAP_TOKEN must be set in the root .env")
			}
			keys, err := identity.LoadOrCreate()
			if err != nil {
				return err
			}
			hash := sha256.Sum256([]byte(keys.SigningPublic))
			deviceID := hex.EncodeToString(hash[:16])
			client := controlplane.Client{BaseURL: serverURL, TLSFingerprint: os.Getenv("ONTHEGO_CONTROL_PLANE_TLS_SHA256")}
			session, err := client.Login(cmd.Context(), controlplane.LoginRequest{BootstrapToken: bootstrap, DeviceID: deviceID, SigningPublic: keys.SigningPublic})
			if err != nil {
				return &otgerror.Error{Code: otgerror.CodeTargetAuth, Message: "control plane login failed", Cause: err}
			}
			if err := controlplane.SaveSession(session); err != nil {
				return err
			}
			return a.print(cmd, map[string]any{"schema_version": 1, "server": session.Server, "device_id": session.DeviceID, "expires_at": session.ExpiresAt}, "logged in to ONTHEGO control plane as device "+session.DeviceID[:12])
		},
	}
	command.Flags().StringVar(&serverURL, "server", "", "control plane URL; defaults to ONTHEGO_CONTROL_PLANE_URL")
	return command
}

func controlPlane(ctx context.Context) (controlplane.Client, controlplane.Session, error) {
	session, err := controlplane.LoadSession()
	if err != nil {
		return controlplane.Client{}, controlplane.Session{}, otgerror.New(otgerror.CodeTargetAuth, "run onthego login first")
	}
	client := controlplane.Client{BaseURL: session.Server, TLSFingerprint: os.Getenv("ONTHEGO_CONTROL_PLANE_TLS_SHA256")}
	if time.Until(session.ExpiresAt) < time.Minute {
		session, err = client.Refresh(ctx, session)
		if err != nil {
			return controlplane.Client{}, controlplane.Session{}, &otgerror.Error{Code: otgerror.CodeTargetAuth, Message: "refresh control plane login", Cause: err}
		}
		if err := controlplane.SaveSession(session); err != nil {
			return controlplane.Client{}, controlplane.Session{}, err
		}
	}
	return client, session, nil
}

func pullControlPlaneState(ctx context.Context, projectID string, local state.ProjectState) (state.ProjectState, error) {
	client, session, err := controlPlane(ctx)
	if err != nil {
		return state.ProjectState{}, err
	}
	document, err := client.GetProject(ctx, session, projectID)
	if err != nil {
		var responseError *controlplane.HTTPError
		if errors.As(err, &responseError) && responseError.StatusCode == 404 {
			return local, nil
		}
		return state.ProjectState{}, err
	}
	var remote state.ProjectState
	if err := json.Unmarshal(document.State, &remote); err != nil {
		return state.ProjectState{}, fmt.Errorf("decode synchronized project state: %w", err)
	}
	if remote.SchemaVersion != 1 || remote.ProjectID != projectID {
		return state.ProjectState{}, errors.New("control plane returned a different project state")
	}
	if remote.Generation > local.Generation {
		return remote, nil
	}
	return local, nil
}

func pushControlPlaneState(ctx context.Context, projectState state.ProjectState) error {
	client, session, err := controlPlane(ctx)
	if err != nil {
		return err
	}
	generation := uint64(0)
	document, err := client.GetProject(ctx, session, projectState.ProjectID)
	if err == nil {
		generation = document.Generation
	} else {
		var responseError *controlplane.HTTPError
		if !errors.As(err, &responseError) || responseError.StatusCode != 404 {
			return err
		}
	}
	_, err = client.PutProject(ctx, session, projectState.ProjectID, generation, projectState)
	return err
}

func (a *app) passCommand() *cobra.Command {
	legacy := a.workloadPassCommand()
	command := &cobra.Command{
		Use:   "pass",
		Args:  cobra.NoArgs,
		Short: "Auto-collect this project's agent sessions and pass work to Daytona",
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, err := selectedProfile(nil)
			if err != nil {
				return err
			}
			return legacy.RunE(cmd, []string{"daytona", profile})
		},
	}
	return command
}

func (a *app) pullCommand() *cobra.Command {
	legacy := a.workloadPullCommand()
	command := &cobra.Command{
		Use:   "pull",
		Args:  cobra.NoArgs,
		Short: "Pull the latest verified Daytona result",
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, err := selectedProfile(nil)
			if err != nil {
				return err
			}
			return legacy.RunE(cmd, []string{"daytona", profile})
		},
	}
	command.Flags().AddFlagSet(legacy.Flags())
	return command
}

func selectedProfile(args []string) (string, error) {
	if len(args) == 1 && args[0] != "" {
		return args[0], nil
	}
	_, cfg, err := loadProject()
	if err != nil {
		return "", err
	}
	if _, ok := cfg.Workloads["agent-sync"]; ok {
		return "agent-sync", nil
	}
	if len(cfg.Workloads) == 1 {
		for name := range cfg.Workloads {
			return name, nil
		}
	}
	return "", otgerror.New(otgerror.CodeInput, "pass a workload profile name")
}

func (a *app) snapshotCommand() *cobra.Command {
	root := &cobra.Command{Use: "snapshot", Short: "Create and restore portable encrypted work snapshots"}
	root.AddCommand(a.snapshotPackCommand(), a.snapshotRestoreCommand())
	return root
}

func (a *app) snapshotPackCommand() *cobra.Command {
	var output, contextDir, goal string
	var include, includeSecret []string
	var noContext, dryRun bool
	command := &cobra.Command{
		Use:   "pack",
		Short: "Capture Git state, selected files, and the agent context envelope",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, cfg, err := loadProject()
			if err != nil {
				return err
			}
			if dryRun {
				files, err := capture.Preview(cmd.Context(), capture.Options{Root: root, Config: cfg, Include: include, IncludeSecret: includeSecret})
				if err != nil {
					return err
				}
				return a.print(cmd, map[string]any{"schema_version": 1, "dry_run": true, "selected_files": files}, fmt.Sprintf("snapshot would include %d selected files plus Git state and the context envelope", len(files)))
			}
			if !noContext && contextDir == "" {
				contextDir, _, err = exportProjectContext(root, cfg, a.version, goal)
				if err != nil {
					return err
				}
			}
			keys, err := identity.LoadOrCreate()
			if err != nil {
				return err
			}
			projectState, err := state.Load(cfg.ProjectID)
			if err != nil {
				return err
			}
			parent := projectState.Environments["local"].LatestSnapshotID
			if output == "" {
				base, err := config.ProjectDataRoot(cfg.ProjectID)
				if err != nil {
					return err
				}
				output = filepath.Join(base, "snapshots", time.Now().UTC().Format("20060102T150405.000000000Z")+".otg")
			}
			result, err := capture.Create(cmd.Context(), capture.Options{Root: root, Config: cfg, Output: output, ContextDir: contextDir, Include: include, IncludeSecret: includeSecret, ParentSnapshotID: parent, EnvironmentID: "local", OnthegoVersion: a.version, Keys: keys})
			if err != nil {
				return err
			}
			expected := projectState.Generation
			projectState.Snapshots[result.Manifest.SnapshotID] = state.Snapshot{ID: result.Manifest.SnapshotID, Parents: result.Manifest.ParentSnapshotIDs, CreatedAt: result.Manifest.CreatedAt, Package: output}
			local := projectState.Environments["local"]
			local.ID = "local"
			local.LatestSnapshotID = result.Manifest.SnapshotID
			local.Dirty = false
			local.ObservedAt = time.Now().UTC()
			projectState.Environments["local"] = local
			if err := state.Save(&projectState, expected); err != nil {
				return err
			}
			return a.print(cmd, map[string]any{"schema_version": 1, "snapshot_id": result.Manifest.SnapshotID, "output": result.Output, "context_envelope_id": result.Manifest.Agent.ContextEnvelopeID}, fmt.Sprintf("created snapshot %s at %s", result.Manifest.SnapshotID[:12], result.Output))
		},
	}
	command.Flags().StringVarP(&output, "output", "o", "", "new .otg package path")
	command.Flags().StringVar(&contextDir, "context-dir", "", "existing context envelope directory")
	command.Flags().StringVar(&goal, "goal", "", "current user goal")
	command.Flags().StringSliceVar(&include, "include", nil, "additional project-relative path or glob")
	command.Flags().StringSliceVar(&includeSecret, "include-secret", nil, "explicit project-relative secret path")
	command.Flags().BoolVar(&noContext, "no-context", false, "capture without an agent context envelope")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "show selection without creating a context or package")
	return command
}

func (a *app) snapshotRestoreCommand() *cobra.Command {
	var output, signer string
	command := &cobra.Command{
		Use:   "restore <snapshot.otg>",
		Args:  cobra.ExactArgs(1),
		Short: "Verify and restore a snapshot into a new worktree",
		RunE: func(cmd *cobra.Command, args []string) error {
			keys, err := identity.LoadOrCreate()
			if err != nil {
				return err
			}
			ageIdentity, err := keys.Identity()
			if err != nil {
				return err
			}
			if signer == "" {
				signer = keys.SigningPublic
			}
			if output == "" {
				output = strings.TrimSuffix(filepath.Base(args[0]), filepath.Ext(args[0])) + "-restored"
			}
			output, err = filepath.Abs(output)
			if err != nil {
				return err
			}
			result, err := restore.Package(cmd.Context(), args[0], output, ageIdentity, signer)
			if err != nil {
				return err
			}
			return a.print(cmd, map[string]any{"schema_version": 1, "snapshot_id": result.Manifest.SnapshotID, "output": result.Root}, fmt.Sprintf("restored snapshot %s to %s", result.Manifest.SnapshotID[:12], result.Root))
		},
	}
	command.Flags().StringVarP(&output, "output", "o", "", "new destination directory")
	command.Flags().StringVar(&signer, "signing-public", "", "trusted Ed25519 public key")
	return command
}

func (a *app) statusCommand() *cobra.Command {
	var refresh, noSummary bool
	command := &cobra.Command{
		Use:   "status",
		Args:  cobra.NoArgs,
		Short: "Show and summarize the current local and Daytona work state",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, cfg, err := loadProject()
			if err != nil {
				return err
			}
			projectState, err := state.Load(cfg.ProjectID)
			if err != nil {
				return err
			}
			projectState, err = pullControlPlaneState(cmd.Context(), cfg.ProjectID, projectState)
			if err != nil {
				return &otgerror.Error{Code: otgerror.CodeTargetAuth, Message: "synchronize control plane state", Cause: err}
			}
			dirty, err := gitx.Dirty(cmd.Context(), root)
			if err != nil {
				return err
			}
			local := projectState.Environments["local"]
			local.Dirty = dirty
			local.ObservedAt = time.Now().UTC()
			projectState.Environments["local"] = local
			receipts, err := workload.ListReceipts(cfg.ProjectID)
			if err != nil {
				return err
			}
			if len(receipts) == 0 && len(projectState.RecentRuns) > 0 {
				if err := json.Unmarshal(projectState.RecentRuns, &receipts); err != nil {
					return fmt.Errorf("decode synchronized run receipts: %w", err)
				}
			}
			if len(receipts) > 5 {
				receipts = receipts[:5]
			}
			logExcerpt := projectState.RecentLogExcerpt
			var refreshError string
			if refresh && len(receipts) > 0 && os.Getenv("DAYTONA_API_KEY") != "" {
				client, clientErr := daytonatransport.New()
				if clientErr == nil {
					observed, _, observeErr := client.Observe(cmd.Context(), receipts[0])
					if observeErr == nil {
						receipts[0] = observed
						_ = workload.SaveReceipt(observed)
						remoteState := projectState.Environments["daytona"]
						remoteState.ID = "daytona"
						remoteState.SandboxID = observed.SandboxID
						remoteState.SessionID = observed.SessionID
						remoteState.CommandID = observed.CommandID
						remoteState.RunState = runState(observed.RunState)
						remoteState.ObservedAt = observed.ObservedAt
						projectState.Environments["daytona"] = remoteState
						logs, logsErr := client.Logs(cmd.Context(), observed)
						if logsErr == nil {
							redacted, _ := contextenv.Redact(logs)
							if len(redacted) > 32<<10 {
								redacted = redacted[len(redacted)-(32<<10):]
							}
							logExcerpt = string(redacted)
						} else {
							refreshError = logsErr.Error()
						}
					} else {
						refreshError = observeErr.Error()
					}
				} else {
					refreshError = clientErr.Error()
				}
			}
			remote := projectState.Environments["daytona"]
			relation := state.Relation(projectState, "local", "daytona")
			summaryText := projectState.LatestSummary.Text
			var summaryError string
			if !noSummary {
				generated, summaryErr := (openaisummary.Client{APIKey: os.Getenv("OPENAI_API_KEY")}).Summarize(cmd.Context(), openaisummary.StatusInput{
					ProjectID:         cfg.ProjectID,
					ActiveEnvironment: projectState.ActiveEnvironmentID,
					Relation:          relation,
					Local:             local,
					Daytona:           remote,
					RecentRuns:        receipts,
					RedactedLog:       logExcerpt,
				})
				if summaryErr != nil {
					summaryError = summaryErr.Error()
				} else {
					summaryText = generated
				}
			}
			projectState.RecentLogExcerpt = logExcerpt
			projectState.RecentRuns, err = json.Marshal(receipts)
			if err != nil {
				return err
			}
			if summaryText != "" {
				projectState.LatestSummary = state.StatusSummary{Text: summaryText, Model: openaisummary.Model, ReasoningEffort: openaisummary.ReasoningEffort, GeneratedAt: time.Now().UTC()}
			}
			expected := projectState.Generation
			if err := state.Save(&projectState, expected); err != nil {
				return err
			}
			if err := pushControlPlaneState(cmd.Context(), projectState); err != nil {
				return &otgerror.Error{Code: otgerror.CodeTargetUnavailable, Message: "publish synchronized status", Cause: err, Retryable: true}
			}
			payload := map[string]any{"schema_version": 1, "active_environment": projectState.ActiveEnvironmentID, "relation": relation, "local": local, "daytona": remote, "recent_runs": receipts, "freshness": "local-live", "summary": summaryText, "summary_model": openaisummary.Model, "summary_reasoning_effort": openaisummary.ReasoningEffort}
			if refreshError != "" {
				payload["refresh_error"] = refreshError
			}
			if summaryError != "" {
				payload["summary_error"] = summaryError
			}
			text := fmt.Sprintf("active: %s\nrelation: %s\nlocal snapshot: %s\nlocal dirty: %t\ndaytona snapshot: %s\ndaytona run: %s", projectState.ActiveEnvironmentID, relation, shortID(local.LatestSnapshotID), local.Dirty, shortID(remote.LatestSnapshotID), remote.RunState)
			if summaryText != "" {
				text += "\n\n" + summaryText
			} else if summaryError != "" {
				text += "\n\nsummary unavailable: " + summaryError
			}
			return a.print(cmd, payload, text)
		},
	}
	command.Flags().BoolVar(&refresh, "refresh", true, "refresh the latest Daytona run and redacted logs")
	command.Flags().BoolVar(&noSummary, "no-summary", false, "skip the GPT-5.6 Luna summary")
	return command
}

func (a *app) versionCommand() *cobra.Command {
	return &cobra.Command{
		Use: "version",
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.print(cmd, map[string]any{"schema_version": 1, "version": a.version, "commit": a.commit, "built_at": a.builtAt}, fmt.Sprintf("onthego %s (%s, %s)", a.version, a.commit, a.builtAt))
		},
	}
}

func (a *app) initCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Args:  cobra.NoArgs,
		Short: "Register the current Git project",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := config.FindRoot(".")
			if err != nil {
				return otgerror.Wrap(otgerror.CodePrecondition, err.Error(), err)
			}
			path := filepath.Join(root, config.FileName)
			cfg, err := config.Load(root)
			created := false
			if err != nil {
				if !os.IsNotExist(err) {
					return otgerror.Wrap(otgerror.CodePrecondition, "load existing project configuration", err)
				}
				cfg = config.Default()
				if err := config.Save(root, cfg); err != nil {
					return err
				}
				created = true
			}
			gitignoreUpdated, err := config.EnsureGitignore(root)
			if err != nil {
				return otgerror.Wrap(otgerror.CodePrecondition, "update .gitignore", err)
			}
			keys, err := loadIdentitySummary()
			if err != nil {
				return err
			}
			message := "initialized ONTHEGO project " + cfg.ProjectID
			if !created {
				message = "ONTHEGO project already initialized " + cfg.ProjectID
			}
			return a.print(cmd, map[string]any{"schema_version": 1, "project_id": cfg.ProjectID, "config": path, "created": created, "gitignore_updated": gitignoreUpdated, "identity": keys}, message)
		},
	}
}

func (a *app) doctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor [daytona]",
		Args:  cobra.MaximumNArgs(1),
		Short: "Check local and target prerequisites",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, cfg, err := loadProject()
			if err != nil {
				return err
			}
			if _, _, err := controlPlane(cmd.Context()); err != nil {
				return err
			}
			checks := []map[string]any{}
			gitVersion, gitErr := gitx.Version(cmd.Context())
			checks = append(checks, check("git", gitVersion, gitErr))
			checks = append(checks, check("repository", root, gitx.Validate(cmd.Context(), root)))
			codexVersion, codexErr := commandVersion(cmd.Context(), "codex", "--version")
			checks = append(checks, check("codex", codexVersion, codexErr))
			if len(args) == 1 {
				if _, ok := cfg.Targets["daytona"]; !ok {
					checks = append(checks, check("daytona target", "", errors.New("target not configured")))
				} else if os.Getenv("DAYTONA_API_KEY") == "" && os.Getenv("DAYTONA_JWT_TOKEN") == "" {
					checks = append(checks, check("daytona auth", "", errors.New("DAYTONA_API_KEY or DAYTONA_JWT_TOKEN is not set")))
				} else {
					client, clientErr := daytonatransport.New()
					if clientErr == nil {
						clientErr = client.Probe(cmd.Context(), cfg.Targets["daytona"].SandboxID)
					}
					checks = append(checks, check("daytona", "credentials present", clientErr))
				}
			}
			failed := false
			for _, item := range checks {
				if item["ok"] == false {
					failed = true
				}
			}
			if err := a.print(cmd, map[string]any{"schema_version": 1, "project_id": cfg.ProjectID, "checks": checks}, renderChecks(checks)); err != nil {
				return err
			}
			if failed {
				return otgerror.New(otgerror.CodePrecondition, "one or more doctor checks failed")
			}
			return nil
		},
	}
}

func (a *app) inspectCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "inspect",
		Short: "Preview Git state and project-scoped agent sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, cfg, err := loadProject()
			if err != nil {
				return err
			}
			status, err := gitx.Status(cmd.Context(), root)
			if err != nil {
				return err
			}
			sessions, err := contextenv.DiscoverAll(root)
			if err != nil {
				return err
			}
			files, err := capture.Preview(cmd.Context(), capture.Options{Root: root, Config: cfg})
			if err != nil {
				return err
			}
			payload := map[string]any{"schema_version": 1, "project_id": cfg.ProjectID, "git_status_porcelain_v2": strings.Split(strings.TrimRight(string(status), "\x00"), "\x00"), "sessions": sessions, "selected_files": files, "default_exclusions": []string{".env and secret candidates", "SSH private keys", "agent auth files", "cloud credential files"}}
			return a.print(cmd, payload, renderInspect(status, sessions, files))
		},
	}
	return command
}

func (a *app) targetCommand() *cobra.Command {
	target := &cobra.Command{Use: "target", Short: "Manage remote targets"}
	var sandbox, image, receiver, ageRecipient, signingPublic string
	add := &cobra.Command{
		Use:  "add daytona",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, cfg, err := loadProject()
			if err != nil {
				return err
			}
			cfg.Targets["daytona"] = config.Target{Provider: "daytona", SandboxID: sandbox, Image: image, ReceiverPath: receiver, AgeRecipient: ageRecipient, SigningPublic: signingPublic}
			if err := config.Save(root, cfg); err != nil {
				return err
			}
			return a.print(cmd, map[string]any{"schema_version": 1, "target": cfg.Targets["daytona"]}, "saved Daytona target")
		},
	}
	add.Flags().StringVar(&sandbox, "sandbox", "", "existing Daytona sandbox ID or name")
	add.Flags().StringVar(&image, "image", "", "default pinned Daytona image")
	add.Flags().StringVar(&receiver, "receiver", daytonatransport.DefaultReceiverPath, "receiver binary path")
	add.Flags().StringVar(&ageRecipient, "age-recipient", "", "authenticated receiver age recipient")
	add.Flags().StringVar(&signingPublic, "signing-public", "", "authenticated receiver Ed25519 public key")
	target.AddCommand(add)
	return target
}

func (a *app) contextCommand() *cobra.Command {
	contextCommand := &cobra.Command{Use: "context", Short: "Export agent context"}
	var goal, output string
	export := &cobra.Command{
		Use:   "export",
		Short: "Create a redacted context envelope and HANDOFF.md",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, cfg, err := loadProject()
			if err != nil {
				return err
			}
			sessions, err := contextenv.DiscoverAll(root)
			if err != nil {
				return err
			}
			result, err := contextenv.ExportAll(cfg.ProjectID, root, a.version, sessions, handoff.Input{Goal: goal, CurrentState: agentDiscoveryState(root, sessions), NextSteps: []string{"Inspect the evidence paths and continue the requested work."}})
			if err != nil {
				return err
			}
			if output == "" {
				base, err := config.ProjectDataRoot(cfg.ProjectID)
				if err != nil {
					return err
				}
				output = filepath.Join(base, "contexts", result.Envelope.ID)
			}
			if err := contextenv.Write(output, result); err != nil {
				return err
			}
			return a.print(cmd, map[string]any{"schema_version": 1, "output": output, "envelope": result.Envelope}, "wrote context envelope to "+output)
		},
	}
	export.Flags().StringVar(&goal, "goal", "", "user goal for the next agent")
	export.Flags().StringVarP(&output, "output", "o", "", "output directory")
	contextCommand.AddCommand(export)
	return contextCommand
}

func (a *app) workloadCommand() *cobra.Command {
	root := &cobra.Command{Use: "workload", Short: "Manage Daytona GPU workloads"}
	root.AddCommand(a.workloadAddCommand(), a.workloadPassCommand(), a.workloadStatusCommand(), a.workloadLogsCommand(), a.workloadStopCommand(), a.workloadPullCommand())
	return root
}

func (a *app) workloadAddCommand() *cobra.Command {
	var kind, image, digest, modelRef, datasetRef, output, checkpoint, health, workdir string
	var gpuTypes, argv, secrets, publish []string
	var gpuCount, port, timeout int
	command := &cobra.Command{
		Use:  "add <name>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, cfg, err := loadProject()
			if err != nil {
				return err
			}
			secretMap, err := parsePairs(secrets)
			if err != nil {
				return err
			}
			value := config.Workload{Kind: kind, Image: image, ImageDigest: digest, GPUCount: gpuCount, GPUTypes: gpuTypes, Argv: argv, Workdir: workdir, ModelRef: modelRef, DatasetRef: datasetRef, OutputPath: output, CheckpointPath: checkpoint, HealthPath: health, Port: port, TimeoutSeconds: timeout, SecretMappings: secretMap, PublishAllowlist: publish}
			definition := workload.FromConfig(args[0], value, "pending")
			if err := definition.Validate(); err != nil {
				return &otgerror.Error{Code: otgerror.CodeInput, Message: err.Error()}
			}
			cfg.Workloads[args[0]] = value
			if err := config.Save(root, cfg); err != nil {
				return err
			}
			return a.print(cmd, map[string]any{"schema_version": 1, "name": args[0], "workload": value}, "saved GPU workload "+args[0])
		},
	}
	command.Flags().StringVar(&kind, "kind", "", "agent, fine_tune, or serve")
	command.Flags().StringVar(&image, "image", "", "container image")
	command.Flags().StringVar(&digest, "image-digest", "", "pinned image digest")
	command.Flags().IntVar(&gpuCount, "gpu", 1, "GPU count")
	command.Flags().StringSliceVar(&gpuTypes, "gpu-type", nil, "preferred GPU types")
	command.Flags().StringArrayVar(&argv, "arg", nil, "workload argv, in order; repeat for each argument")
	command.Flags().StringVar(&workdir, "workdir", "/workspace", "sandbox work directory")
	command.Flags().StringVar(&modelRef, "model", "", "hf:// model reference with revision")
	command.Flags().StringVar(&datasetRef, "dataset", "", "hf:// dataset reference with revision")
	command.Flags().StringVar(&output, "output", "output/result.tar.zst", "relative result artifact")
	command.Flags().StringVar(&checkpoint, "checkpoint", "", "relative checkpoint artifact")
	command.Flags().StringVar(&health, "health", "", "serving health path")
	command.Flags().IntVar(&port, "port", 0, "serving port")
	command.Flags().IntVar(&timeout, "timeout", 3600, "timeout in seconds")
	command.Flags().StringSliceVar(&secrets, "secret", nil, "ENV=daytona-secret-name")
	command.Flags().StringSliceVar(&publish, "publish-allow", nil, "allowed Hugging Face repository")
	return command
}

func (a *app) workloadPassCommand() *cobra.Command {
	var goal, contextDir string
	command := &cobra.Command{
		Use:  "pass daytona <name>",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "daytona" {
				return otgerror.New(otgerror.CodeInput, "Phase 1 supports the daytona workload target")
			}
			root, cfg, err := loadProject()
			if err != nil {
				return err
			}
			value, ok := cfg.Workloads[args[1]]
			if !ok {
				return otgerror.New(otgerror.CodeInput, "unknown workload "+args[1])
			}
			envelopeID := "manual"
			if contextDir == "" {
				sessions, discoverErr := contextenv.DiscoverAll(root)
				if discoverErr != nil {
					return discoverErr
				}
				currentState := append([]string{"Workload: " + args[1]}, agentDiscoveryState(root, sessions)...)
				exported, exportErr := contextenv.ExportAll(cfg.ProjectID, root, a.version, sessions, handoff.Input{Goal: goal, CurrentState: currentState, NextSteps: []string{"Run the workload and preserve logs and receipts."}})
				if exportErr != nil {
					return exportErr
				}
				base, baseErr := config.ProjectDataRoot(cfg.ProjectID)
				if baseErr != nil {
					return baseErr
				}
				contextDir = filepath.Join(base, "contexts", exported.Envelope.ID)
				if writeErr := contextenv.Write(contextDir, exported); writeErr != nil {
					return writeErr
				}
				envelopeID = exported.Envelope.ID
			} else {
				envelopeID, err = readContextEnvelopeID(contextDir, cfg.ProjectID)
				if err != nil {
					return err
				}
			}
			definition := workload.FromConfig(args[1], value, envelopeID)
			resolver := huggingface.Client{}
			if definition.ModelRef != "" {
				resolved, resolveErr := resolver.Resolve(cmd.Context(), definition.ModelRef)
				if resolveErr != nil {
					return resolveErr
				}
				definition.ModelCommit = resolved.Commit
			}
			if definition.DatasetRef != "" {
				resolved, resolveErr := resolver.Resolve(cmd.Context(), definition.DatasetRef)
				if resolveErr != nil {
					return resolveErr
				}
				definition.DatasetCommit = resolved.Commit
			}
			projectState, err := state.Load(cfg.ProjectID)
			if err != nil {
				return err
			}
			request := workload.NewRequest(cfg.ProjectID, projectState.Generation, definition)
			remoteRoot := "/workspace/onthego/" + cfg.ProjectID + "/" + request.TransferID
			request.Definition.Workdir = remoteRoot + "/workspace"
			request.Definition.OutputPath, err = remoteArtifact(remoteRoot, value.OutputPath)
			if err != nil {
				return err
			}
			if value.CheckpointPath != "" {
				request.Definition.CheckpointPath, err = remoteArtifact(remoteRoot, value.CheckpointPath)
				if err != nil {
					return err
				}
			}
			for env, secretName := range request.Definition.SecretMappings {
				binding, exists := cfg.Secrets[secretName]
				if !exists || binding.Name != secretName {
					return otgerror.New(otgerror.CodeSecretPolicy, "secret "+secretName+" is not registered in project policy")
				}
				if request.Definition.SecretAllowedHosts == nil {
					request.Definition.SecretAllowedHosts = map[string][]string{}
				}
				request.Definition.SecretAllowedHosts[env] = append([]string(nil), binding.Hosts...)
			}
			target, ok := cfg.Targets["daytona"]
			if !ok {
				return otgerror.New(otgerror.CodePrecondition, "configure target add daytona first")
			}
			client, err := daytonatransport.New()
			if err != nil {
				return err
			}
			receipt, err := client.PassWorkload(cmd.Context(), target, request, contextDir)
			if err != nil {
				return err
			}
			if err := workload.SaveReceipt(receipt); err != nil {
				return err
			}
			expected := projectState.Generation
			remote := projectState.Environments["daytona"]
			remote.ID = "daytona"
			remote.SandboxID = receipt.SandboxID
			remote.SessionID = receipt.SessionID
			remote.CommandID = receipt.CommandID
			remote.RunState = state.AgentRunning
			remote.ObservedAt = receipt.ObservedAt
			projectState.Environments["daytona"] = remote
			projectState.ActiveEnvironmentID = "daytona"
			projectState.RecentRuns, err = json.Marshal([]workload.Receipt{receipt})
			if err != nil {
				return err
			}
			if err := state.Save(&projectState, expected); err != nil {
				return err
			}
			if err := pushControlPlaneState(cmd.Context(), projectState); err != nil {
				return &otgerror.Error{Code: otgerror.CodeTargetUnavailable, Message: "publish passed state", Cause: err, Retryable: true}
			}
			return a.print(cmd, map[string]any{"schema_version": 1, "receipt": receipt}, fmt.Sprintf("started %s on Daytona sandbox %s, run %s", args[1], receipt.SandboxID, receipt.CommandID))
		},
	}
	command.Flags().StringVar(&goal, "goal", "", "workload goal")
	command.Flags().StringVar(&contextDir, "context-dir", "", "existing context envelope directory")
	return command
}

func (a *app) workloadStatusCommand() *cobra.Command {
	return &cobra.Command{Use: "status daytona <name>", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, receipt, client, err := loadRun(cmd.Context(), args)
		if err != nil {
			return err
		}
		observed, status, err := client.Observe(cmd.Context(), receipt)
		if err != nil {
			return err
		}
		if err := workload.SaveReceipt(observed); err != nil {
			return err
		}
		projectState, err := state.Load(cfg.ProjectID)
		if err != nil {
			return err
		}
		expected := projectState.Generation
		remote := projectState.Environments["daytona"]
		remote.ID = "daytona"
		remote.SandboxID = observed.SandboxID
		remote.SessionID = observed.SessionID
		remote.CommandID = observed.CommandID
		remote.RunState = runState(observed.RunState)
		remote.ObservedAt = observed.ObservedAt
		projectState.Environments["daytona"] = remote
		if err := state.Save(&projectState, expected); err != nil {
			return err
		}
		return a.print(cmd, map[string]any{"schema_version": 1, "project_id": cfg.ProjectID, "receipt": observed, "provider_status": status}, fmt.Sprintf("%s: %s on %s", observed.WorkloadName, observed.RunState, observed.SandboxID))
	}}
}

func (a *app) workloadLogsCommand() *cobra.Command {
	return &cobra.Command{Use: "logs daytona <name>", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, receipt, client, err := loadRun(cmd.Context(), args)
		if err != nil {
			return err
		}
		data, err := client.Logs(cmd.Context(), receipt)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(append(data, '\n'))
		return err
	}}
}

func (a *app) workloadStopCommand() *cobra.Command {
	return &cobra.Command{Use: "stop daytona <name>", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, receipt, client, err := loadRun(cmd.Context(), args)
		if err != nil {
			return err
		}
		if err := client.Stop(cmd.Context(), receipt); err != nil {
			return err
		}
		receipt.RunState = "STOPPED"
		receipt.ObservedAt = time.Now().UTC()
		if err := workload.SaveReceipt(receipt); err != nil {
			return err
		}
		return a.print(cmd, map[string]any{"schema_version": 1, "receipt": receipt}, "stopped ONTHEGO workload session "+receipt.SessionID)
	}}
}

func (a *app) workloadPullCommand() *cobra.Command {
	var output string
	command := &cobra.Command{Use: "pull daytona <name>", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, receipt, client, err := loadRun(cmd.Context(), args)
		if err != nil {
			return err
		}
		if _, _, err := controlPlane(cmd.Context()); err != nil {
			return err
		}
		observed, _, err := client.Observe(cmd.Context(), receipt)
		if err != nil {
			return err
		}
		if observed.RunState == "RUNNING" {
			return otgerror.New(otgerror.CodePrecondition, "workload is still running; wait or stop it before pull")
		}
		remote := observed.OutputPath
		if remote == "" {
			remote = observed.CheckpointPath
		}
		if remote == "" {
			return otgerror.New(otgerror.CodeInput, "workload has no configured result artifact")
		}
		if output == "" {
			output = filepath.Base(remote)
		}
		if err := client.Download(cmd.Context(), observed, remote, output); err != nil {
			return err
		}
		expectedHash := observed.ArtifactSHA256[remote]
		if expectedHash == "" {
			_ = os.Remove(output)
			return otgerror.New(otgerror.CodePackageInvalid, "remote receipt did not include the selected artifact hash")
		}
		actualHash, err := sha256File(output)
		if err != nil {
			_ = os.Remove(output)
			return err
		}
		if actualHash != expectedHash {
			_ = os.Remove(output)
			return otgerror.New(otgerror.CodePackageInvalid, "downloaded workload artifact hash does not match the remote receipt")
		}
		if err := workload.SaveReceipt(observed); err != nil {
			return err
		}
		projectState, err := state.Load(cfg.ProjectID)
		if err != nil {
			return err
		}
		expected := projectState.Generation
		remoteState := projectState.Environments["daytona"]
		remoteState.ID = "daytona"
		remoteState.SandboxID = observed.SandboxID
		remoteState.SessionID = observed.SessionID
		remoteState.CommandID = observed.CommandID
		remoteState.RunState = runState(observed.RunState)
		remoteState.ObservedAt = observed.ObservedAt
		projectState.Environments["daytona"] = remoteState
		projectState.ActiveEnvironmentID = "local"
		projectState.RecentRuns, err = json.Marshal([]workload.Receipt{observed})
		if err != nil {
			return err
		}
		if err := state.Save(&projectState, expected); err != nil {
			return err
		}
		if err := pushControlPlaneState(cmd.Context(), projectState); err != nil {
			return &otgerror.Error{Code: otgerror.CodeTargetUnavailable, Message: "publish pulled state", Cause: err, Retryable: true}
		}
		return a.print(cmd, map[string]any{"schema_version": 1, "receipt": observed, "output": output}, "downloaded verified workload artifact to "+output)
	}}
	command.Flags().StringVarP(&output, "output", "o", "", "new local output path")
	return command
}

func (a *app) modelCommand() *cobra.Command {
	root := &cobra.Command{Use: "model", Short: "Resolve Hugging Face model and dataset references"}
	root.AddCommand(&cobra.Command{Use: "resolve <hf-ref>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		resolved, err := (huggingface.Client{}).Resolve(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return a.print(cmd, map[string]any{"schema_version": 1, "resolved": resolved}, fmt.Sprintf("%s resolved to %s", args[0], resolved.Commit))
	}})
	return root
}

func (a *app) receiveCommand() *cobra.Command {
	root := &cobra.Command{Use: "receive", Hidden: true}
	var request string
	workloadCommand := &cobra.Command{Use: "workload", RunE: func(cmd *cobra.Command, args []string) error {
		return receiver.RunWorkload(cmd.Context(), request, cmd.OutOrStdout(), cmd.ErrOrStderr())
	}}
	workloadCommand.Flags().StringVar(&request, "request", "", "absolute workload request path")
	_ = workloadCommand.MarkFlagRequired("request")
	root.AddCommand(workloadCommand)
	return root
}

func (a *app) print(cmd *cobra.Command, payload any, text string) error {
	if a.json {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetEscapeHTML(false)
		return encoder.Encode(payload)
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), text)
	return err
}

func loadProject() (string, config.Config, error) {
	root, err := config.FindRoot(".")
	if err != nil {
		return "", config.Config{}, otgerror.Wrap(otgerror.CodePrecondition, err.Error(), err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", config.Config{}, otgerror.New(otgerror.CodePrecondition, "project is not initialized; run onthego init")
		}
		return "", config.Config{}, otgerror.Wrap(otgerror.CodePrecondition, "load project configuration", err)
	}
	return root, cfg, nil
}

func loadRun(ctx context.Context, args []string) (config.Config, workload.Receipt, *daytonatransport.Client, error) {
	if len(args) != 2 || args[0] != "daytona" {
		return config.Config{}, workload.Receipt{}, nil, otgerror.New(otgerror.CodeInput, "Phase 1 supports the daytona workload target")
	}
	_, cfg, err := loadProject()
	if err != nil {
		return config.Config{}, workload.Receipt{}, nil, err
	}
	receipt, err := workload.LatestReceipt(cfg.ProjectID, args[1])
	if err != nil {
		projectState, stateErr := state.Load(cfg.ProjectID)
		if stateErr != nil {
			return config.Config{}, workload.Receipt{}, nil, stateErr
		}
		projectState, stateErr = pullControlPlaneState(ctx, cfg.ProjectID, projectState)
		if stateErr != nil {
			return config.Config{}, workload.Receipt{}, nil, stateErr
		}
		var receipts []workload.Receipt
		if json.Unmarshal(projectState.RecentRuns, &receipts) != nil {
			return config.Config{}, workload.Receipt{}, nil, otgerror.Wrap(otgerror.CodePrecondition, "no workload receipt found", err)
		}
		found := false
		for _, candidate := range receipts {
			if candidate.WorkloadName == args[1] {
				receipt = candidate
				found = true
				break
			}
		}
		if !found {
			return config.Config{}, workload.Receipt{}, nil, otgerror.Wrap(otgerror.CodePrecondition, "no workload receipt found", err)
		}
		if err := workload.SaveReceipt(receipt); err != nil {
			return config.Config{}, workload.Receipt{}, nil, err
		}
	}
	client, err := daytonatransport.New()
	return cfg, receipt, client, err
}

func parsePairs(values []string) (map[string]string, error) {
	result := map[string]string{}
	for _, value := range values {
		key, item, ok := strings.Cut(value, "=")
		if !ok || key == "" || item == "" {
			return nil, otgerror.New(otgerror.CodeInput, "expected KEY=VALUE, got "+value)
		}
		result[key] = item
	}
	return result, nil
}

func remoteArtifact(remoteRoot, relative string) (string, error) {
	if relative == "" {
		return "", nil
	}
	if filepath.IsAbs(relative) {
		return "", otgerror.New(otgerror.CodeInput, "artifact path must be relative to the workload workspace")
	}
	clean := filepath.Clean(relative)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", otgerror.New(otgerror.CodeInput, "artifact path escapes the workload workspace")
	}
	return remoteRoot + "/workspace/" + filepath.ToSlash(clean), nil
}

func check(name, value string, err error) map[string]any {
	item := map[string]any{"name": name, "ok": err == nil}
	if value != "" {
		item["value"] = value
	}
	if err != nil {
		item["error"] = err.Error()
	}
	return item
}

func commandVersion(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	data, err := command.CombinedOutput()
	return strings.TrimSpace(string(data)), err
}

func renderChecks(checks []map[string]any) string {
	var out strings.Builder
	for _, item := range checks {
		status := "ok"
		if item["ok"] == false {
			status = "failed"
		}
		fmt.Fprintf(&out, "%s: %s", item["name"], status)
		if value, ok := item["value"].(string); ok && value != "" {
			fmt.Fprintf(&out, " (%s)", value)
		}
		if message, ok := item["error"].(string); ok {
			fmt.Fprintf(&out, ": %s", message)
		}
		out.WriteString("\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

func renderInspect(status []byte, sessions []contextenv.Session, files []capture.PreviewFile) string {
	var out strings.Builder
	out.WriteString("Git state:\n")
	for _, item := range strings.Split(strings.TrimRight(string(status), "\x00"), "\x00") {
		if item != "" {
			out.WriteString("  " + item + "\n")
		}
	}
	out.WriteString("Project agent sessions:\n")
	for _, session := range sessions {
		fmt.Fprintf(&out, "  %s  %s  %s  %d bytes\n", session.AgentKind, session.ID, session.UpdatedAt.Format(time.RFC3339), session.SizeBytes)
	}
	if len(sessions) == 0 {
		out.WriteString("  none\n")
	}
	out.WriteString("Selected additional files:\n")
	for _, file := range files {
		fmt.Fprintf(&out, "  %s  policy=%s  secret=%t\n", file.RelativePath, file.Policy, file.Secret)
	}
	if len(files) == 0 {
		out.WriteString("  none\n")
	}
	out.WriteString("Default exclusions: .env and secret candidates, SSH private keys, agent auth files, cloud credential files\n")
	return strings.TrimRight(out.String(), "\n")
}

func loadIdentitySummary() (map[string]string, error) {
	// Delayed import through a tiny helper keeps private key material out of command output.
	keys, err := identityKeys()
	if err != nil {
		return nil, err
	}
	return map[string]string{"age_recipient": keys[0], "signing_public": keys[1]}, nil
}

func identityKeys() ([2]string, error) {
	return identitySummary()
}

func exportProjectContext(root string, cfg config.Config, version, goal string) (string, contextenv.ExportResult, error) {
	sessions, err := contextenv.DiscoverAll(root)
	if err != nil {
		return "", contextenv.ExportResult{}, err
	}
	result, err := contextenv.ExportAll(cfg.ProjectID, root, version, sessions, handoff.Input{
		Goal:         goal,
		CurrentState: agentDiscoveryState(root, sessions),
		NextSteps:    []string{"Read HANDOFF.md and the redacted evidence before continuing the task."},
	})
	if err != nil {
		return "", contextenv.ExportResult{}, err
	}
	base, err := config.ProjectDataRoot(cfg.ProjectID)
	if err != nil {
		return "", contextenv.ExportResult{}, err
	}
	output := filepath.Join(base, "contexts", result.Envelope.ID)
	if err := contextenv.Write(output, result); err != nil {
		return "", contextenv.ExportResult{}, err
	}
	return output, result, nil
}

func agentDiscoveryState(root string, sessions []contextenv.Session) []string {
	kinds := contextenv.Kinds(sessions)
	agents := "none"
	if len(kinds) > 0 {
		agents = strings.Join(kinds, ", ")
	}
	return []string{
		"Source workspace: " + root,
		fmt.Sprintf("Automatically collected %d project sessions from: %s", len(sessions), agents),
	}
}

func shortID(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	if value == "" {
		return "none"
	}
	return value
}

func readContextEnvelopeID(dir, projectID string) (string, error) {
	file, err := os.Open(filepath.Join(dir, "context-envelope.json"))
	if err != nil {
		return "", otgerror.Wrap(otgerror.CodeSessionFormat, "read context envelope", err)
	}
	defer file.Close()
	var envelope contextenv.Envelope
	decoder := json.NewDecoder(io.LimitReader(file, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return "", otgerror.Wrap(otgerror.CodeSessionFormat, "decode context envelope", err)
	}
	if envelope.SchemaVersion != contextenv.SchemaVersion || envelope.ID == "" || envelope.ProjectID != projectID {
		return "", otgerror.New(otgerror.CodeSessionFormat, "context envelope belongs to another project or schema")
	}
	return envelope.ID, nil
}

func runState(value string) state.AgentState {
	switch value {
	case "RUNNING":
		return state.AgentRunning
	case "SUCCEEDED":
		return state.AgentSucceeded
	case "FAILED":
		return state.AgentFailed
	case "STOPPED":
		return state.AgentStopped
	default:
		return state.AgentNotStarted
	}
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
