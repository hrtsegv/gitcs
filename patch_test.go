package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func patchTestRepo(t *testing.T) (string, *git.Repository) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("native Git is required for patch integration tests")
	}
	path := t.TempDir()
	return path, createTestRepo(t, path, "tester@test.com", 0, testClock)
}

func patchTestFile(t *testing.T, repo *git.Repository, path, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(path, name), []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add(name); err != nil {
		t.Fatal(err)
	}
}

func patchTestLoad(t *testing.T, hash plumbing.Hash, paths ...string) CommitPatch {
	t.Helper()
	patch, err := loadCommitPatch(context.Background(), hash, paths)
	if err != nil {
		t.Fatal(err)
	}
	return patch
}

func TestCommitPatchRootEditsAndDeletion(t *testing.T) {
	path, repo := patchTestRepo(t)
	patchTestFile(t, repo, path, "file with spaces.txt", "original\n")
	patchTestFile(t, repo, path, "removed.txt", "remove me\n")
	root := addTestCommit(t, repo, "root", "tester@test.com", testClock, testClock)
	patch := patchTestLoad(t, root, path)
	if patch.Merge || patch.Truncated || patch.Repository != path || !strings.Contains(patch.Text, "+original\n") || !strings.Contains(patch.Text, "new file mode") {
		t.Fatalf("root patch: %+v", patch)
	}
	patchTestFile(t, repo, path, "file with spaces.txt", "replacement\n")
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Remove("removed.txt"); err != nil {
		t.Fatal(err)
	}
	changed := addTestCommit(t, repo, "edit and delete", "tester@test.com", testClock, testClock)
	patch = patchTestLoad(t, changed, path)
	for _, want := range []string{"-original\n", "+replacement\n", "deleted file mode", "-remove me\n"} {
		if !strings.Contains(patch.Text, want) {
			t.Fatalf("missing %q in edit patch:\n%s", want, patch.Text)
		}
	}
	empty := addTestCommit(t, repo, "empty", "tester@test.com", testClock, testClock)
	if patch := patchTestLoad(t, empty, path); patch.Text != "" || patch.Truncated {
		t.Fatalf("empty commit has patch: %+v", patch)
	}
}

func TestCommitPatchMergeUsesFirstParent(t *testing.T) {
	path, repo := patchTestRepo(t)
	patchTestFile(t, repo, path, "base.txt", "base\n")
	base := addTestCommit(t, repo, "base", "tester@test.com", testClock, testClock)
	patchTestFile(t, repo, path, "main.txt", "main-only\n")
	first := addTestCommit(t, repo, "main", "tester@test.com", testClock, testClock)
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Hash: base}); err != nil {
		t.Fatal(err)
	}
	patchTestFile(t, repo, path, "feature.txt", "feature-only\n")
	feature := addTestCommit(t, repo, "feature", "tester@test.com", testClock, testClock)
	if err := worktree.Checkout(&git.CheckoutOptions{Hash: first}); err != nil {
		t.Fatal(err)
	}
	patchTestFile(t, repo, path, "feature.txt", "feature-only\n")
	merge, err := worktree.Commit("merge", &git.CommitOptions{
		Parents: []plumbing.Hash{first, feature},
		Author:  &object.Signature{Name: "Tester", Email: "tester@test.com", When: testClock},
	})
	if err != nil {
		t.Fatal(err)
	}
	patch := patchTestLoad(t, merge, path)
	if !patch.Merge || !strings.Contains(patch.Text, "+feature-only\n") || strings.Contains(patch.Text, "main-only") {
		t.Fatalf("merge must compare only with first parent:\n%+v", patch)
	}
}

func TestCommitPatchBinaryModeAndNoRenames(t *testing.T) {
	path, repo := patchTestRepo(t)
	patchTestFile(t, repo, path, "binary.dat", "\x00before")
	patchTestFile(t, repo, path, "old.txt", "renamed\n")
	patchTestFile(t, repo, path, "script", "echo hello\n")
	addTestCommit(t, repo, "before", "tester@test.com", testClock, testClock)
	patchTestFile(t, repo, path, "binary.dat", "\x00after")
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Remove("old.txt"); err != nil {
		t.Fatal(err)
	}
	patchTestFile(t, repo, path, "new.txt", "renamed\n")
	gitCommand(t, path, "update-index", "--chmod=+x", "script")
	gitCommand(t, path, "config", "diff.renames", "true")
	hash := addTestCommit(t, repo, "binary mode rename", "tester@test.com", testClock, testClock)
	patch := patchTestLoad(t, hash, path)
	for _, want := range []string{"Binary files", "old mode 100644", "new mode 100755", "deleted file mode", "new file mode"} {
		if !strings.Contains(patch.Text, want) {
			t.Fatalf("missing %q in patch:\n%s", want, patch.Text)
		}
	}
	if strings.Contains(patch.Text, "similarity index") || strings.Contains(patch.Text, "rename from") {
		t.Fatalf("rename detection was not disabled:\n%s", patch.Text)
	}
}

func TestCommitPatchWorktreeAndFallback(t *testing.T) {
	path, repo := patchTestRepo(t)
	patchTestFile(t, repo, path, "file.txt", "linked worktree\n")
	hash := addTestCommit(t, repo, "linked", "tester@test.com", testClock, testClock)
	linked := filepath.Join(t.TempDir(), "linked")
	gitCommand(t, path, "worktree", "add", "--detach", linked, "HEAD")
	patch := patchTestLoad(t, hash, filepath.Join(t.TempDir(), "missing"), linked)
	if patch.Repository != linked || !strings.Contains(patch.Text, "+linked worktree\n") {
		t.Fatalf("fallback worktree patch: %+v", patch)
	}
	// Ensure loading a patch did not retain the worktree's common-directory file.
	metadata := filepath.Join(path, ".git", "worktrees", "linked", "commondir")
	if err := os.Rename(metadata, metadata+".closed"); err != nil {
		t.Fatalf("worktree metadata retained after patch: %v", err)
	}
}

func TestCommitPatchShallowParentUnavailable(t *testing.T) {
	path, repo := patchTestRepo(t)
	patchTestFile(t, repo, path, "file.txt", "before\n")
	addTestCommit(t, repo, "before", "tester@test.com", testClock, testClock)
	patchTestFile(t, repo, path, "file.txt", "after\n")
	hash := addTestCommit(t, repo, "after", "tester@test.com", testClock, testClock)
	shallow := t.TempDir()
	if _, err := git.PlainClone(shallow, false, &git.CloneOptions{URL: path, Depth: 1}); err != nil {
		t.Fatal(err)
	}
	if patch, err := loadCommitPatch(context.Background(), hash, []string{shallow}); err == nil || !strings.Contains(err.Error(), "first parent") || patch.Text != "" {
		t.Fatalf("missing shallow parent was shown as root: %+v, %v", patch, err)
	}
	patch := patchTestLoad(t, hash, shallow, path)
	if patch.Repository != path || !strings.Contains(patch.Text, "-before\n+after\n") {
		t.Fatalf("did not fall back to complete history: %+v", patch)
	}
	// Mark a boundary whose parent is present. Tree comparisons still show edits.
	if err := repo.Storer.SetShallow([]plumbing.Hash{hash}); err != nil {
		t.Fatal(err)
	}
	if patch = patchTestLoad(t, hash, path); !strings.Contains(patch.Text, "-before\n+after\n") || strings.Contains(patch.Text, "new file mode") {
		t.Fatalf("available shallow parent was treated as root: %+v", patch)
	}
}

func TestCommitPatchDisablesHelpersAndReplaceObjects(t *testing.T) {
	path, repo := patchTestRepo(t)
	patchTestFile(t, repo, path, ".gitattributes", "*.txt diff=unavailable\n")
	patchTestFile(t, repo, path, "file.txt", "raw original\n")
	root := addTestCommit(t, repo, "root", "tester@test.com", testClock, testClock)
	patchTestFile(t, repo, path, "file.txt", "raw changed\n")
	changed := addTestCommit(t, repo, "change", "tester@test.com", testClock, testClock)
	gitCommand(t, path, "config", "diff.external", "gitcs-nonexistent-diff-helper")
	gitCommand(t, path, "config", "diff.unavailable.command", "gitcs-nonexistent-driver")
	gitCommand(t, path, "config", "diff.unavailable.textconv", "gitcs-nonexistent-textconv")
	gitCommand(t, path, "replace", changed.String(), root.String())
	t.Setenv("GIT_EXTERNAL_DIFF", "gitcs-nonexistent-environment-helper")
	t.Setenv("GIT_PAGER", "gitcs-nonexistent-pager")
	patch := patchTestLoad(t, changed, path)
	if !strings.Contains(patch.Text, "-raw original\n+raw changed\n") {
		t.Fatalf("replacement or helper affected raw object diff:\n%s", patch.Text)
	}
}

func TestCommitPatchIgnoresRepositoryRoutingEnvironment(t *testing.T) {
	path, repo := patchTestRepo(t)
	patchTestFile(t, repo, path, "file.txt", "intended repository\n")
	hash := addTestCommit(t, repo, "root", "tester@test.com", testClock, testClock)
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_SHALLOW_FILE", "GIT_NAMESPACE"} {
		t.Setenv(name, filepath.Join(t.TempDir(), "wrong"))
	}
	patch := patchTestLoad(t, hash, path)
	if !strings.Contains(patch.Text, "+intended repository\n") {
		t.Fatalf("inherited environment redirected Git: %+v", patch)
	}
	// Windows environment names are case-insensitive, even when inherited with
	// mixed casing. Verify filtering without relying on the host's environment.
	env := patchEnvironment([]string{"Git_Dir=wrong", "git_work_tree=wrong", "PATH=kept"})
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "GIT_DIR") || strings.EqualFold(name, "GIT_WORK_TREE") {
			t.Fatalf("mixed-case routing environment survived: %v", env)
		}
	}
}

func TestCommitPatchOutputCapAndCancellation(t *testing.T) {
	path, repo := patchTestRepo(t)
	patchTestFile(t, repo, path, "large.txt", strings.Repeat("a large patch line\n", maxPatchBytes/8))
	hash := addTestCommit(t, repo, "large", "tester@test.com", testClock, testClock)
	started := time.Now()
	patch := patchTestLoad(t, hash, path)
	if !patch.Truncated || len(patch.Text) != maxPatchBytes {
		t.Fatalf("patch output was not bounded: truncated=%v bytes=%d", patch.Truncated, len(patch.Text))
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("truncated patch did not stop promptly: %s", elapsed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loadCommitPatch(ctx, hash, []string{path}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := loadCommitPatch(ctx, hash, []string{path}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lost: %v", err)
	}
}

func TestCommitPatchMissingGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := loadCommitPatch(context.Background(), plumbing.ZeroHash, []string{t.TempDir()}); err == nil || !strings.Contains(err.Error(), "install Git") {
		t.Fatalf("missing Git error is not actionable: %v", err)
	}
}
