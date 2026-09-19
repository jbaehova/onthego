package receiver

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
	"syscall"
	"time"

	"github.com/jbaehova/onthego/internal/workload"
)

func RunWorkload(ctx context.Context, requestPath string, stdout, stderr io.Writer) error {
	requestPath = filepath.Clean(requestPath)
	if !filepath.IsAbs(requestPath) || !strings.HasPrefix(requestPath, "/workspace/onthego/") {
		return errors.New("request path is outside the ONTHEGO workspace")
	}
	file, err := os.Open(requestPath)
	if err != nil {
		return err
	}
	defer file.Close()
	var request workload.Request
	decoder := json.NewDecoder(io.LimitReader(file, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("decode workload request: %w", err)
	}
	if request.SchemaVersion != workload.SchemaVersion || request.ProjectID == "" || request.TransferID == "" {
		return errors.New("invalid workload request identity")
	}
	if err := request.Definition.Validate(); err != nil {
		return err
	}
	for _, artifact := range []string{request.Definition.OutputPath, request.Definition.CheckpointPath} {
		if artifact == "" {
			continue
		}
		clean := filepath.Clean(artifact)
		root := filepath.Clean(request.Definition.Workdir) + string(filepath.Separator)
		if !strings.HasPrefix(clean, root) {
			return errors.New("artifact path is outside the workload workspace")
		}
	}
	requestHash, err := request.SHA256()
	if err != nil {
		return err
	}
	metadataDir := filepath.Dir(requestPath)
	receiptPath := filepath.Join(metadataDir, "workload-receipt.json")
	if existing, err := readReceipt(receiptPath); err == nil {
		if existing.RequestSHA256 != requestHash {
			return errors.New("transfer ID was already used with another workload request")
		}
		return json.NewEncoder(stdout).Encode(existing)
	}
	if err := os.MkdirAll(request.Definition.Workdir, 0o700); err != nil {
		return err
	}
	logsDir := filepath.Join(metadataDir, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		return err
	}
	stdoutFile, err := os.OpenFile(filepath.Join(logsDir, "stdout.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer stdoutFile.Close()
	stderrFile, err := os.OpenFile(filepath.Join(logsDir, "stderr.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer stderrFile.Close()
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(request.Definition.TimeoutSeconds)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, request.Definition.Argv[0], request.Definition.Argv[1:]...)
	cmd.Dir = request.Definition.Workdir
	cmd.Env = restrictedEnvironment(request.Definition.SecretMappings)
	cmd.Stdout = io.MultiWriter(stdout, stdoutFile)
	cmd.Stderr = io.MultiWriter(stderr, stderrFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	receipt := workload.Receipt{
		SchemaVersion:  workload.SchemaVersion,
		ProjectID:      request.ProjectID,
		TransferID:     request.TransferID,
		WorkloadName:   request.Definition.Name,
		Kind:           request.Definition.Kind,
		PID:            cmd.Process.Pid,
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
	}
	if err := writeReceipt(receiptPath, receipt); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		return err
	}
	err = cmd.Wait()
	receipt.ObservedAt = time.Now().UTC()
	if err == nil {
		receipt.RunState = "SUCCEEDED"
		receipt.ArtifactSHA256 = map[string]string{}
		for _, path := range []string{request.Definition.OutputPath, request.Definition.CheckpointPath} {
			if path == "" {
				continue
			}
			hash, hashErr := hashArtifact(path)
			if hashErr != nil {
				return fmt.Errorf("hash workload artifact %s: %w", path, hashErr)
			}
			receipt.ArtifactSHA256[path] = hash
		}
	} else {
		receipt.RunState = "FAILED"
	}
	if writeErr := writeReceipt(receiptPath, receipt); writeErr != nil {
		return writeErr
	}
	if err != nil {
		return fmt.Errorf("workload failed: %w", err)
	}
	return json.NewEncoder(stdout).Encode(receipt)
}

func hashArtifact(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("artifact is not a regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, 10<<30)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func restrictedEnvironment(secretMappings map[string]string) []string {
	allowed := []string{"PATH", "HOME", "LANG", "LC_ALL", "CUDA_VISIBLE_DEVICES", "HF_HOME", "TRANSFORMERS_CACHE"}
	seen := map[string]bool{}
	result := make([]string, 0, len(allowed)+8)
	for _, key := range allowed {
		if value, ok := os.LookupEnv(key); ok {
			result = append(result, key+"="+value)
			seen[key] = true
		}
	}
	for key := range secretMappings {
		if value, ok := os.LookupEnv(key); ok && !seen[key] {
			result = append(result, key+"="+value)
		}
	}
	return result
}

func readReceipt(path string) (workload.Receipt, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return workload.Receipt{}, err
	}
	var receipt workload.Receipt
	err = json.Unmarshal(data, &receipt)
	return receipt, err
}

func writeReceipt(path string, receipt workload.Receipt) error {
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "receipt-*.tmp")
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
	return os.Rename(name, path)
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
