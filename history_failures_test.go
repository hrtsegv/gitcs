package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func looseObjectPath(path string, hash plumbing.Hash) string {
	hex := hash.String()
	return filepath.Join(path, ".git", "objects", hex[:2], hex[2:])
}

func TestHistoryObjectFailuresRetainOtherRepositories(t *testing.T) {
	for _, failure := range []string{"missing tip", "missing ancestor", "corrupt ancestor", "branch points to missing object"} {
		t.Run(failure, func(t *testing.T) {
			good, broken := t.TempDir(), t.TempDir()
			createTestRepo(t, good, "tester@test.com", 2, testClock.Add(-time.Hour))
			repo := createTestRepo(t, broken, "tester@test.com", 0, testClock)
			ancestor := addTestCommit(t, repo, "ancestor", "tester@test.com", testClock, testClock)
			tip := addTestCommit(t, repo, "tip", "tester@test.com", testClock, testClock)
			switch failure {
			case "missing tip":
				if err := os.Remove(looseObjectPath(broken, tip)); err != nil {
					t.Fatal(err)
				}
			case "missing ancestor":
				if err := os.Remove(looseObjectPath(broken, ancestor)); err != nil {
					t.Fatal(err)
				}
			case "corrupt ancestor":
				if err := os.WriteFile(looseObjectPath(broken, ancestor), []byte("not a zlib Git object"), 0644); err != nil {
					t.Fatal(err)
				}
			case "branch points to missing object":
				if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("broken"), plumbing.NewHash("deadbeef"))); err != nil {
					t.Fatal(err)
				}
			}
			report := queryRepos(t, []string{broken, good}, testQuery(t, "", "", "UTC"))
			if report.Processed != 1 || len(report.Commits) != 2 || report.Days["2026-10-16"] != 2 {
				t.Fatalf("failed history inflated or hid valid counts: %+v", report)
			}
			if len(report.Diagnostics) != 1 || report.Diagnostics[0].Path != broken || report.Diagnostics[0].Err == nil {
				t.Fatalf("corruption did not identify the failed repository: %+v", report.Diagnostics)
			}
		})
	}
}

func TestHistoryMissingBlobDoesNotHideCommitMetadata(t *testing.T) {
	path := t.TempDir()
	repo := createTestRepo(t, path, "tester@test.com", 0, testClock)
	patchTestFile(t, repo, path, "file.txt", "lost content\n")
	hash := addTestCommit(t, repo, "metadata is intact", "tester@test.com", testClock, testClock)
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
	report := queryRepos(t, []string{path}, testQuery(t, "", "", "UTC"))
	if report.Processed != 1 || len(report.Diagnostics) != 0 || len(report.Commits) != 1 || report.Commits[0].Hash != hash {
		t.Fatalf("unrelated missing file object hid available commit metadata: %+v", report)
	}
}

func TestHistoryMergeVisitsBothParentsDespiteOldAuthorDate(t *testing.T) {
	path := t.TempDir()
	repo := createTestRepo(t, path, "tester@test.com", 0, testClock)
	baseWhen := mustTime(t, "2026-10-10T12:00:00Z")
	base := addTestCommit(t, repo, "shared ancestor", "tester@test.com", baseWhen, testClock)
	first := addTestCommit(t, repo, "first parent", "tester@test.com", baseWhen.Add(48*time.Hour), testClock)
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Hash: base}); err != nil {
		t.Fatal(err)
	}
	second := addTestCommit(t, repo, "second parent", "tester@test.com", baseWhen.Add(24*time.Hour), testClock)
	if err := worktree.Checkout(&git.CheckoutOptions{Hash: first}); err != nil {
		t.Fatal(err)
	}
	_, err = worktree.Commit("merge with an older author clock", &git.CommitOptions{
		AllowEmptyCommits: true,
		Parents:           []plumbing.Hash{first, second},
		Author:            &object.Signature{Name: "Tester", Email: "tester@test.com", When: baseWhen.AddDate(0, -1, 0)},
		Committer:         &object.Signature{Name: "Tester", Email: "tester@test.com", When: testClock},
	})
	if err != nil {
		t.Fatal(err)
	}
	q := testQuery(t, "2026-10-10", "2026-10-12", "UTC")
	q.BranchScope = HeadBranch
	report := queryRepos(t, []string{path}, q)
	if len(report.Diagnostics) != 0 || len(report.Commits) != 3 {
		t.Fatalf("merge ancestry was pruned or shared ancestor counted twice: %+v", report)
	}
	for _, day := range []string{"2026-10-10", "2026-10-11", "2026-10-12"} {
		if report.Days[day] != 1 {
			t.Fatalf("merge parent day %s: got %v", day, report.Days)
		}
	}
}

func TestHistoryInvalidScopeAndWorktreeMetadata(t *testing.T) {
	path := t.TempDir()
	createTestRepo(t, path, "tester@test.com", 1, testClock)
	q := testQuery(t, "", "", "UTC")
	q.BranchScope = "unknown"
	if _, err := readRepo(context.Background(), path, q); err == nil {
		t.Fatal("invalid branch scope was accepted")
	}
	linked := filepath.Join(t.TempDir(), "linked")
	gitCommand(t, path, "worktree", "add", "--detach", linked, "HEAD")
	metadata := filepath.Join(path, ".git", "worktrees", "linked", "commondir")
	if err := os.WriteFile(metadata, []byte("missing-common-directory\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := openRepository(linked); err != git.ErrRepositoryIncomplete {
		t.Fatalf("broken worktree common directory: %v", err)
	}
	// A failed open must release the same commondir handle as a successful scan.
	if err := os.Rename(metadata, metadata+".closed"); err != nil {
		t.Fatalf("failed worktree open retained its metadata: %v", err)
	}
}
