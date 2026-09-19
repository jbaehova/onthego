package daytona

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	daytonasdk "github.com/daytona/clients/sdk-go/pkg/daytona"
	daytonaoptions "github.com/daytona/clients/sdk-go/pkg/options"
	"github.com/daytona/clients/sdk-go/pkg/types"
	"github.com/jbaehova/onthego/internal/config"
	"github.com/jbaehova/onthego/internal/otgerror"
	"github.com/jbaehova/onthego/internal/workload"
)

// DefaultReceiverPath is the configured receiver location for a Daytona target.
// PassWorkload uploads the bundled receiver under the individual transfer root.
const DefaultReceiverPath = "/usr/local/bin/onthego"

// Client adapts the official Daytona Go SDK to ONTHEGO workload receipts.
// SDK owns the authenticated Daytona API and sandbox Toolbox clients.
type Client struct {
	SDK *daytonasdk.Client
}

// New initializes the official Daytona SDK without duplicating its auth flow.
// Repository .env loading happens in the CLI before this boundary is reached.
func New() (*Client, error) {
	sdk, err := daytonasdk.NewClient()
	if err != nil {
		return nil, &otgerror.Error{Code: otgerror.CodeTargetAuth, Message: "Daytona authentication failed", Cause: err}
	}
	return &Client{SDK: sdk}, nil
}

// Probe checks Daytona access without creating a sandbox or starting a process.
// An empty sandboxID checks the account's sandbox listing instead.
func (c *Client) Probe(ctx context.Context, sandboxID string) error {
	if sandboxID == "" {
		iterator := c.SDK.List(ctx, nil)
		_ = iterator.Next()
		if err := iterator.Err(); err != nil {
			return &otgerror.Error{Code: otgerror.CodeTargetUnavailable, Message: "cannot list Daytona sandboxes", Cause: err, Retryable: true}
		}
		return nil
	}
	_, err := c.SDK.Get(ctx, sandboxID)
	if err != nil {
		return &otgerror.Error{Code: otgerror.CodeTargetUnavailable, Message: "cannot reach configured Daytona sandbox", Cause: err, Retryable: true}
	}
	return nil
}

// PassWorkload performs the Daytona handoff: resolve a sandbox, upload the
// request and redacted context, upload the receiver, then launch its process.
// The returned Daytona command ID identifies an asynchronous run, not a
// completed result. Observe establishes completion and retrieves its receipt.
func (c *Client) PassWorkload(ctx context.Context, target config.Target, request workload.Request, contextDir string) (workload.Receipt, error) {
	if err := request.Definition.Validate(); err != nil {
		return workload.Receipt{}, &otgerror.Error{Code: otgerror.CodeInput, Message: err.Error()}
	}
	sandbox, err := c.ensureSandbox(ctx, target, request)
	if err != nil {
		return workload.Receipt{}, err
	}
	remoteRoot := fmt.Sprintf("/workspace/onthego/%s/%s", request.ProjectID, request.TransferID)
	metadataRoot := remoteRoot + "/metadata"
	if err := sandbox.FileSystem.CreateFolder(ctx, metadataRoot); err != nil {
		return workload.Receipt{}, targetError("create remote metadata directory", err)
	}
	data, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return workload.Receipt{}, err
	}
	requestPath := metadataRoot + "/workload-request.json"
	if err := sandbox.FileSystem.UploadFileStream(ctx, bytes.NewReader(data), requestPath); err != nil {
		return workload.Receipt{}, targetError("upload workload request", err)
	}
	if contextDir != "" {
		if err := uploadDirectory(ctx, sandbox, contextDir, remoteRoot+"/context"); err != nil {
			return workload.Receipt{}, targetError("upload context envelope", err)
		}
	}
	receiver := target.ReceiverPath
	if receiver == "" {
		receiver = DefaultReceiverPath
	}
	localReceiver, err := receiverBinary()
	if err != nil {
		return workload.Receipt{}, targetError("locate ONTHEGO receiver", err)
	}
	if localReceiver != "" {
		remoteReceiver := remoteRoot + "/bin/onthego"
		if err := uploadExecutable(ctx, sandbox, localReceiver, remoteReceiver); err != nil {
			return workload.Receipt{}, targetError("upload ONTHEGO receiver", err)
		}
		receiver = remoteReceiver
	}
	sessionID := "onthego-" + strings.ReplaceAll(request.TransferID, "-", "")[:16]
	if err := sandbox.Process.CreateSession(ctx, sessionID); err != nil {
		return workload.Receipt{}, targetError("create Daytona process session", err)
	}
	if receiver != filepath.Clean(receiver) || !filepath.IsAbs(receiver) || strings.ContainsAny(receiver, "\n\r\t '") {
		return workload.Receipt{}, &otgerror.Error{Code: otgerror.CodeInput, Message: "receiver path is not a safe absolute path"}
	}
	command := receiver + " receive workload --request " + requestPath
	result, err := sandbox.Process.ExecuteSessionCommand(ctx, sessionID, command, true, true)
	if err != nil {
		return workload.Receipt{}, targetError("start Daytona workload receiver", err)
	}
	commandID, _ := result["id"].(string)
	if commandID == "" {
		return workload.Receipt{}, &otgerror.Error{Code: otgerror.CodeRunUncertain, Message: "Daytona returned no command ID", Retryable: true}
	}
	requestHash, err := request.SHA256()
	if err != nil {
		return workload.Receipt{}, err
	}
	return workload.Receipt{
		SchemaVersion:  workload.SchemaVersion,
		ProjectID:      request.ProjectID,
		TransferID:     request.TransferID,
		WorkloadName:   request.Definition.Name,
		Kind:           request.Definition.Kind,
		SandboxID:      sandbox.ID,
		SessionID:      sessionID,
		CommandID:      commandID,
		RequestSHA256:  requestHash,
		RunState:       "RUNNING",
		ImageDigest:    request.Definition.ImageDigest,
		GPUType:        first(request.Definition.GPUTypes),
		ModelCommit:    request.Definition.ModelCommit,
		DatasetCommit:  request.Definition.DatasetCommit,
		OutputPath:     request.Definition.OutputPath,
		CheckpointPath: request.Definition.CheckpointPath,
		CreatedAt:      time.Now().UTC(),
		ObservedAt:     time.Now().UTC(),
	}, nil
}

// receiverBinary selects the Linux amd64 executable used inside Daytona.
// The npm package bundles it alongside the local platform's CLI executable.
func receiverBinary() (string, error) {
	if configured := os.Getenv("ONTHEGO_RECEIVER_BINARY"); configured != "" {
		if !isLinuxAMD64(configured) {
			return "", errors.New("ONTHEGO_RECEIVER_BINARY must be a Linux amd64 executable")
		}
		return configured, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	candidates := []string{
		filepath.Join(filepath.Dir(executable), "onthego-linux-amd64"),
		executable,
	}
	for _, candidate := range candidates {
		if isLinuxAMD64(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("bundled Linux amd64 receiver is missing")
}

func isLinuxAMD64(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	header := make([]byte, 20)
	if _, err := io.ReadFull(file, header); err != nil {
		return false
	}
	return bytes.Equal(header[:4], []byte{0x7f, 'E', 'L', 'F'}) && header[18] == 0x3e && header[19] == 0x00
}

// uploadExecutable uses Daytona FileSystem streaming and permission APIs.
// It avoids requiring a compiler or package installer in the Daytona sandbox.
func uploadExecutable(ctx context.Context, sandbox *daytonasdk.Sandbox, localPath, remotePath string) error {
	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 200<<20 {
		return errors.New("receiver binary must be a regular file smaller than 200 MiB")
	}
	if err := sandbox.FileSystem.CreateFolder(ctx, filepath.Dir(remotePath)); err != nil {
		return err
	}
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	uploadErr := sandbox.FileSystem.UploadFileStream(ctx, file, remotePath)
	closeErr := file.Close()
	if uploadErr != nil {
		return uploadErr
	}
	if closeErr != nil {
		return closeErr
	}
	return sandbox.FileSystem.SetFilePermissions(ctx, remotePath, daytonaoptions.WithPermissionMode("0755"))
}

// Observe combines Daytona Process status with the ONTHEGO receiver receipt.
// The SDK's process exit code and the receiver's transfer identity are separate
// checks: a completed Daytona command must still match the original handoff.
func (c *Client) Observe(ctx context.Context, receipt workload.Receipt) (workload.Receipt, map[string]any, error) {
	sandbox, err := c.SDK.Get(ctx, receipt.SandboxID)
	if err != nil {
		return receipt, nil, targetError("get Daytona sandbox", err)
	}
	status, err := sandbox.Process.GetSessionCommand(ctx, receipt.SessionID, receipt.CommandID)
	if err != nil {
		return receipt, nil, targetError("get Daytona command", err)
	}
	receipt.ObservedAt = time.Now().UTC()
	if code, ok := numericExitCode(status["exitCode"]); ok {
		if code == 0 {
			receipt.RunState = "SUCCEEDED"
			remoteReceipt, receiptErr := readRemoteReceipt(ctx, sandbox, receipt)
			if receiptErr != nil {
				return receipt, status, targetError("read workload receipt", receiptErr)
			}
			if remoteReceipt.RequestSHA256 != receipt.RequestSHA256 || remoteReceipt.TransferID != receipt.TransferID {
				return receipt, status, &otgerror.Error{Code: otgerror.CodePackageInvalid, Message: "remote workload receipt does not match the local request"}
			}
			receipt.PID = remoteReceipt.PID
			receipt.ArtifactSHA256 = remoteReceipt.ArtifactSHA256
			receipt.Endpoint = remoteReceipt.Endpoint
			receipt.ServedModel = remoteReceipt.ServedModel
			receipt.Health = remoteReceipt.Health
			receipt.HubCommit = remoteReceipt.HubCommit
		} else {
			receipt.RunState = "FAILED"
		}
	} else {
		receipt.RunState = "RUNNING"
	}
	return receipt, status, nil
}

// readRemoteReceipt streams bounded metadata from the Daytona transfer folder.
// The receiver writes this document after executing the configured workload.
func readRemoteReceipt(ctx context.Context, sandbox *daytonasdk.Sandbox, receipt workload.Receipt) (workload.Receipt, error) {
	path := fmt.Sprintf("/workspace/onthego/%s/%s/metadata/workload-receipt.json", receipt.ProjectID, receipt.TransferID)
	stream, err := sandbox.FileSystem.DownloadFileStream(ctx, path)
	if err != nil {
		return workload.Receipt{}, err
	}
	defer stream.Close()
	var remote workload.Receipt
	decoder := json.NewDecoder(io.LimitReader(stream, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&remote); err != nil {
		return workload.Receipt{}, err
	}
	return remote, nil
}

// Logs retrieves stdout and stderr for the recorded Daytona session command.
// Callers must redact this output before displaying or synchronizing it.
func (c *Client) Logs(ctx context.Context, receipt workload.Receipt) ([]byte, error) {
	sandbox, err := c.SDK.Get(ctx, receipt.SandboxID)
	if err != nil {
		return nil, targetError("get Daytona sandbox", err)
	}
	logs, err := sandbox.Process.GetSessionCommandLogs(ctx, receipt.SessionID, receipt.CommandID)
	if err != nil {
		return nil, targetError("get Daytona logs", err)
	}
	return json.MarshalIndent(logs, "", "  ")
}

// Stop deletes an ONTHEGO-owned Daytona process session. It does not delete
// the Daytona sandbox or unrelated sessions sharing that sandbox.
func (c *Client) Stop(ctx context.Context, receipt workload.Receipt) error {
	if !strings.HasPrefix(receipt.SessionID, "onthego-") {
		return &otgerror.Error{Code: otgerror.CodeInput, Message: "refusing to stop a session not created by ONTHEGO"}
	}
	sandbox, err := c.SDK.Get(ctx, receipt.SandboxID)
	if err != nil {
		return targetError("get Daytona sandbox", err)
	}
	if err := sandbox.Process.DeleteSession(ctx, receipt.SessionID); err != nil {
		return targetError("stop Daytona session", err)
	}
	return nil
}

// Download retrieves a file from the project's managed Daytona directory.
// Local files are created exclusively with private permissions. Hash checking
// belongs to the workload pull layer, which has the expected artifact digest.
func (c *Client) Download(ctx context.Context, receipt workload.Receipt, remotePath, localPath string) error {
	if !strings.HasPrefix(filepath.Clean(remotePath), "/workspace/onthego/"+receipt.ProjectID+"/") {
		return errors.New("remote artifact path is outside the managed workload root")
	}
	sandbox, err := c.SDK.Get(ctx, receipt.SandboxID)
	if err != nil {
		return targetError("get Daytona sandbox", err)
	}
	stream, err := sandbox.FileSystem.DownloadFileStream(ctx, remotePath)
	if err != nil {
		return targetError("download Daytona artifact", err)
	}
	defer stream.Close()
	if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(localPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(stream, 10<<30)); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ensureSandbox reuses a configured Daytona sandbox or creates an ephemeral
// sandbox with ONTHEGO project labels. Secret mappings are delegated to
// Daytona's sandbox configuration; local .env contents are not uploaded here.
func (c *Client) ensureSandbox(ctx context.Context, target config.Target, request workload.Request) (*daytonasdk.Sandbox, error) {
	if target.SandboxID != "" {
		sandbox, err := c.SDK.Get(ctx, target.SandboxID)
		if err != nil {
			return nil, targetError("get configured Daytona sandbox", err)
		}
		return sandbox, nil
	}
	definition := request.Definition
	autoDelete := 0
	image := definition.Image
	if target.Image != "" {
		image = target.Image
	}
	params := types.ImageParams{
		Image: image,
		Resources: &types.Resources{
			GPU:     definition.GPUCount,
			GpuType: gpuTypes(definition.GPUTypes),
		},
		SandboxBaseParams: types.SandboxBaseParams{
			Name:               "onthego-" + request.TransferID[:12],
			Language:           types.CodeLanguagePython,
			Labels:             map[string]string{"managed-by": "onthego", "project-id": request.ProjectID},
			Secrets:            definition.SecretMappings,
			Ephemeral:          true,
			AutoDeleteInterval: &autoDelete,
		},
	}
	sandbox, err := c.SDK.Create(ctx, params)
	if err != nil {
		return nil, targetError("create Daytona sandbox", err)
	}
	return sandbox, nil
}

// uploadDirectory transfers the prepared context through Daytona FileSystem.
// Only regular files and directories are accepted; symlinks are not followed.
func uploadDirectory(ctx context.Context, sandbox *daytonasdk.Sandbox, localRoot, remoteRoot string) error {
	return filepath.WalkDir(localRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(localRoot, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("context path escaped upload root")
		}
		remote := remoteRoot
		if rel != "." {
			remote += "/" + filepath.ToSlash(rel)
		}
		if entry.IsDir() {
			return sandbox.FileSystem.CreateFolder(ctx, remote)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("context contains unsupported non-regular file: %s", rel)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		uploadErr := sandbox.FileSystem.UploadFileStream(ctx, file, remote)
		closeErr := file.Close()
		if uploadErr != nil {
			return uploadErr
		}
		return closeErr
	})
}

func gpuTypes(values []string) []types.GpuType {
	result := make([]types.GpuType, 0, len(values))
	for _, value := range values {
		switch strings.ToUpper(strings.ReplaceAll(value, "-", "_")) {
		case "H100":
			result = append(result, types.GpuTypeH100)
		case "RTX_PRO_6000", "RTXPRO6000":
			result = append(result, types.GpuTypeRtxPro6000)
		case "MI355X":
			result = append(result, types.GpuTypeMI355X)
		}
	}
	return result
}

func numericExitCode(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func targetError(action string, err error) error {
	return &otgerror.Error{Code: otgerror.CodeTargetUnavailable, Message: action, Cause: err, Retryable: true}
}
