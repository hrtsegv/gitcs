package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
)

const (
	maxPatchBytes = 1 << 20
	patchTimeout  = 30 * time.Second
	maxPatchError = 8 << 10
)

type CommitPatch struct {
	Repository string
	Text       string
	Truncated  bool
	Merge      bool
}

// loadCommitPatch only runs when requested by the explorer. Comparing tree IDs
// explicitly preserves first-parent semantics even at a shallow boundary.
func loadCommitPatch(ctx context.Context, hash plumbing.Hash, paths []string) (CommitPatch, error) {
	if err := ctx.Err(); err != nil {
		return CommitPatch{}, err
	}
	if len(paths) == 0 {
		return CommitPatch{}, errors.New("this commit has no repository locations; refresh the explorer and retry")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return CommitPatch{}, fmt.Errorf("Git is required to view patches; install Git and retry: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, patchTimeout)
	defer cancel()
	var failures []string
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return CommitPatch{}, fmt.Errorf("patch loading stopped: %w", err)
		}
		trees, merge, err := patchTrees(path, hash)
		if err == nil {
			var patch CommitPatch
			patch, err = runCommitPatch(ctx, gitPath, path, trees)
			if err == nil {
				patch.Merge = merge
				return patch, nil
			}
		}
		if len(failures) < 3 {
			failures = append(failures, fmt.Sprintf("%q: %v", path, err))
		}
	}
	if err := ctx.Err(); err != nil {
		return CommitPatch{}, fmt.Errorf("patch loading stopped: %w", err)
	}
	suffix := ""
	if len(paths) > len(failures) {
		suffix = fmt.Sprintf(" (and %d other repository locations)", len(paths)-len(failures))
	}
	return CommitPatch{}, fmt.Errorf("could not load this commit's patch: %s%s", strings.Join(failures, "; "), suffix)
}

// patchTrees releases storage before starting Git, including worktree common
// directory handles on Windows. An unavailable parent must never become a root.
func patchTrees(path string, hash plumbing.Hash) ([]string, bool, error) {
	repo, err := openRepository(path)
	if err != nil {
		return nil, false, err
	}
	if closer, ok := repo.Storer.(io.Closer); ok {
		defer closer.Close()
	}
	commit, err := repo.CommitObject(hash)
	if err != nil {
		return nil, false, fmt.Errorf("commit %s is unavailable: %w", hash, err)
	}
	if len(commit.ParentHashes) == 0 {
		return []string{hash.String()}, false, nil
	}
	parent, err := repo.CommitObject(commit.ParentHashes[0])
	if err != nil {
		return nil, false, fmt.Errorf("first parent %s is unavailable (history may be shallow); use a complete local clone or deepen this repository: %w", commit.ParentHashes[0], err)
	}
	return []string{parent.TreeHash.String(), commit.TreeHash.String()}, len(commit.ParentHashes) > 1, nil
}

func runCommitPatch(ctx context.Context, gitPath, path string, trees []string) (CommitPatch, error) {
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := []string{
		"--no-pager", "--no-replace-objects", "-C", path,
		"-c", "protocol.allow=never", // Older Git versions may ignore GIT_NO_LAZY_FETCH.
		"-c", "core.quotePath=true",
		"diff-tree", "--no-commit-id", "--root", "-r", "-p",
		"--no-ext-diff", "--no-textconv", "--no-renames", "--no-color",
		"--submodule=short", "--src-prefix=a/", "--dst-prefix=b/",
	}
	args = append(args, trees...)
	args = append(args, "--")
	command := exec.CommandContext(commandCtx, gitPath, args...)
	command.Env = patchEnvironment(os.Environ())
	// No stdin is inherited: neither Git nor a configured helper can consume UI keys.
	command.WaitDelay = time.Second
	stdout := patchOutput{limit: maxPatchBytes, stop: cancel}
	stderr := patchOutput{limit: maxPatchError}
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if ctx.Err() != nil {
		return CommitPatch{}, ctx.Err()
	}
	if stdout.truncated {
		return CommitPatch{Repository: path, Text: string(stdout.data), Truncated: true}, nil
	}
	if err != nil {
		message := strings.TrimSpace(string(stderr.data))
		if message != "" {
			return CommitPatch{}, fmt.Errorf("Git could not produce a patch: %w: %s", err, message)
		}
		return CommitPatch{}, fmt.Errorf("Git could not produce a patch: %w", err)
	}
	return CommitPatch{Repository: path, Text: string(stdout.data)}, nil
}

// patchOutput never retains more than limit bytes. The first excess byte stops
// patch generation; stderr uses the same bound while discarding excess output.
type patchOutput struct {
	data      []byte
	limit     int
	truncated bool
	stop      context.CancelFunc
}

func (w *patchOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := w.limit - len(w.data)
	w.data = append(w.data, p[:min(remaining, n)]...)
	if n > remaining && !w.truncated {
		w.truncated = true
		if w.stop != nil {
			w.stop()
		}
	}
	return n, nil
}

func patchEnvironment(environ []string) []string {
	env := make([]string, 0, len(environ)+4)
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
			"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_SHALLOW_FILE", "GIT_NAMESPACE",
			"GIT_NO_LAZY_FETCH", "GIT_OPTIONAL_LOCKS", "GIT_TERMINAL_PROMPT", "GIT_ALLOW_PROTOCOL":
			continue
		}
		env = append(env, entry)
	}
	return append(env, "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=")
}
