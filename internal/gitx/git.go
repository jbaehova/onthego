package gitx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jbaehova/onthego/internal/otgerror"
)

type Result struct {
	Stdout []byte
	Stderr []byte
}

func Run(ctx context.Context, root string, args ...string) (Result, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, nil
}

func Version(ctx context.Context) (string, error) {
	command := exec.CommandContext(ctx, "git", "--version")
	data, err := command.Output()
	return strings.TrimSpace(string(data)), err
}

func Validate(ctx context.Context, root string) error {
	short, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	inside, err := Run(short, root, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(string(inside.Stdout)) != "true" {
		return otgerror.Wrap(otgerror.CodeUnsupportedGit, "not a supported Git worktree", err)
	}
	if _, err := Run(short, root, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		return otgerror.Wrap(otgerror.CodeUnsupportedGit, "repository must have a HEAD commit", err)
	}
	shallow, err := Run(short, root, "rev-parse", "--is-shallow-repository")
	if err == nil && strings.TrimSpace(string(shallow.Stdout)) == "true" {
		return otgerror.New(otgerror.CodeUnsupportedGit, "shallow repositories are not supported")
	}
	unmerged, err := Run(short, root, "ls-files", "-u", "-z")
	if err != nil {
		return err
	}
	if len(unmerged.Stdout) > 0 {
		return otgerror.New(otgerror.CodeUnsupportedGit, "repositories with unmerged index entries are not supported")
	}
	if _, err := os.Stat(root + "/.gitmodules"); err == nil {
		return otgerror.New(otgerror.CodeUnsupportedGit, "submodules are not supported in Phase 1")
	}
	sparse, err := Run(short, root, "config", "--bool", "core.sparseCheckout")
	if err == nil && strings.TrimSpace(string(sparse.Stdout)) == "true" {
		return otgerror.New(otgerror.CodeUnsupportedGit, "sparse checkout is not supported in Phase 1")
	}
	partial, err := Run(short, root, "config", "--local", "--get-regexp", `^(extensions\.partialClone|remote\..*\.promisor)$`)
	if err == nil && len(strings.TrimSpace(string(partial.Stdout))) > 0 {
		return otgerror.New(otgerror.CodeUnsupportedGit, "partial clones are not supported in Phase 1")
	}
	filters, err := Run(short, root, "config", "--local", "--get-regexp", `^filter\.`)
	if err == nil && len(strings.TrimSpace(string(filters.Stdout))) > 0 {
		return otgerror.New(otgerror.CodeUnsupportedGit, "repository Git filters are not supported in Phase 1")
	}
	lfs, err := Run(short, root, "grep", "-I", "-n", "filter=lfs", "--", "*.gitattributes")
	if err == nil && len(strings.TrimSpace(string(lfs.Stdout))) > 0 {
		return otgerror.New(otgerror.CodeUnsupportedGit, "Git LFS is not supported in Phase 1")
	}
	return nil
}

func Status(ctx context.Context, root string) ([]byte, error) {
	result, err := Run(ctx, root, "-c", "core.quotepath=false", "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all")
	return result.Stdout, err
}

func Dirty(ctx context.Context, root string) (bool, error) {
	status, err := Status(ctx, root)
	if err != nil {
		return false, err
	}
	for _, item := range bytes.Split(status, []byte{0}) {
		if len(item) > 0 && item[0] != '#' {
			return true, nil
		}
	}
	return false, nil
}
