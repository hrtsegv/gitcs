package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// The test executable also provides a portable subprocess fixture. Dispatch
// before testing parses Git's arguments, and only for a patch invocation.
func TestMain(m *testing.M) {
	if mode := os.Getenv("GITCS_PATCH_TEST_PROCESS"); mode != "" && slices.Contains(os.Args, "--no-pager") {
		switch mode {
		case "wait":
			if err := os.WriteFile(os.Getenv("GITCS_PATCH_TEST_STARTED"), []byte("started"), 0600); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(99)
			}
			for {
				time.Sleep(time.Hour)
			}
		case "failure":
			fmt.Fprint(os.Stderr, strings.Repeat("x", 4*maxPatchError), "discarded tail")
			os.Exit(7)
		}
		os.Exit(98)
	}
	os.Exit(m.Run())
}

func TestCommitPatchActiveProcessCancellationAndDeadline(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, deadline := range []bool{false, true} {
		name := "cancel"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("GITCS_PATCH_TEST_PROCESS", "wait")
			marker := t.TempDir() + string(os.PathSeparator) + "started"
			t.Setenv("GITCS_PATCH_TEST_STARTED", marker)
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			}
			defer cancel()
			path := t.TempDir()
			done := make(chan error, 1)
			go func() {
				_, err := runCommitPatch(ctx, executable, path, []string{plumbing.ZeroHash.String()})
				done <- err
			}()
			started := time.NewTicker(10 * time.Millisecond)
			defer started.Stop()
			startupTimeout := time.NewTimer(10 * time.Second)
			defer startupTimeout.Stop()
			ready := false
			for !ready {
				select {
				case <-started.C:
					_, err := os.Stat(marker)
					ready = err == nil
				case err := <-done:
					t.Fatalf("patch process exited before cancellation could be tested: %v", err)
				case <-startupTimeout.C:
					t.Fatal("patch subprocess never started")
				}
			}
			want := context.Canceled
			if deadline {
				want = context.DeadlineExceeded
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("active subprocess lost %v: %v", want, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("patch process did not stop after its context ended")
			}
		})
	}
}

func TestCommitPatchSubprocessFailureBoundsStderr(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITCS_PATCH_TEST_PROCESS", "failure")
	patch, err := runCommitPatch(context.Background(), executable, t.TempDir(), []string{plumbing.ZeroHash.String()})
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 || !strings.Contains(err.Error(), "Git could not produce a patch") {
		t.Fatalf("subprocess failure was swallowed: %+v, %v", patch, err)
	}
	if len(err.Error()) > maxPatchError+256 || strings.Contains(err.Error(), "discarded tail") || patch.Text != "" {
		t.Fatalf("subprocess stderr was not bounded: patch=%+v error bytes=%d", patch, len(err.Error()))
	}
}

func TestCommitPatchMissingBlobAndCommitFallback(t *testing.T) {
	path, repo := patchTestRepo(t)
	patchTestFile(t, repo, path, "file.txt", "available in another clone\n")
	hash := addTestCommit(t, repo, "root", "tester@test.com", testClock, testClock)
	clone := t.TempDir()
	if _, err := git.PlainClone(clone, false, &git.CloneOptions{URL: path}); err != nil {
		t.Fatal(err)
	}
	commit, err := repo.CommitObject(hash)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(looseObjectPath(path, tree.Entries[0].Hash)); err != nil {
		t.Fatal(err)
	}
	// Git may reuse an unchanged working-tree file when its blob is missing.
	// Remove that recovery source too so the first membership truly lacks data.
	if err := os.Remove(filepath.Join(path, "file.txt")); err != nil {
		t.Fatal(err)
	}
	if patch, err := loadCommitPatch(context.Background(), hash, []string{path}); err == nil || patch.Text != "" || !strings.Contains(err.Error(), "Git could not produce a patch") {
		t.Fatalf("missing blob displayed a successful or partial patch: %+v, %v", patch, err)
	}
	patch := patchTestLoad(t, hash, path, clone)
	if patch.Repository != clone || !strings.Contains(patch.Text, "+available in another clone\n") {
		t.Fatalf("Git failure prevented fallback to complete membership: %+v", patch)
	}
	if err := os.Remove(looseObjectPath(path, hash)); err != nil {
		t.Fatal(err)
	}
	patch = patchTestLoad(t, hash, path, clone)
	if patch.Repository != clone || !strings.Contains(patch.Text, "+available in another clone\n") {
		t.Fatalf("missing commit prevented fallback to complete membership: %+v", patch)
	}
}

func TestCommitPatchUnavailableLocations(t *testing.T) {
	if _, err := loadCommitPatch(context.Background(), plumbing.ZeroHash, nil); err == nil || !strings.Contains(err.Error(), "no repository locations") {
		t.Fatalf("no locations error was not actionable: %v", err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("native Git is required for repository fallback errors")
	}
	paths := []string{t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()}
	if patch, err := loadCommitPatch(context.Background(), plumbing.ZeroHash, paths); err == nil || patch.Text != "" || !strings.Contains(err.Error(), "2 other repository locations") || strings.Contains(err.Error(), paths[3]) {
		t.Fatalf("unavailable-location diagnostics were not bounded: %+v, %v", patch, err)
	}
}
