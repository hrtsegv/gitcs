package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

func TestInclusiveEndDate(t *testing.T) {
	path := t.TempDir()
	repo := createTestRepo(t, path, "tester@test.com", 0, testClock)
	for _, stamp := range []string{
		"2026-10-09T23:59:59.999999999Z",
		"2026-10-10T00:00:00Z",
		"2026-10-10T23:59:59.999999999Z",
		"2026-10-11T00:00:00Z",
	} {
		addTestCommit(t, repo, stamp, "tester@test.com", mustTime(t, stamp), testClock)
	}
	report := queryRepos(t, []string{path}, testQuery(t, "2026-10-10", "2026-10-10", "UTC"))
	if len(report.Commits) != 2 || report.Days["2026-10-10"] != 2 {
		t.Fatalf("inclusive day did not match midnight and late evening: %+v", report)
	}
}

func TestAuthorDateAndTimezone(t *testing.T) {
	path := t.TempDir()
	repo := createTestRepo(t, path, "tester@test.com", 0, testClock)
	old := mustTime(t, "2026-09-01T12:00:00Z")
	inside := mustTime(t, "2026-10-10T23:30:00Z") // 11 October in Beirut.
	addTestCommit(t, repo, "old author recently rebased", "tester@test.com", old, inside)
	addTestCommit(t, repo, "recent author old committer", "tester@test.com", inside, old)
	report := queryRepos(t, []string{path}, testQuery(t, "2026-10-11", "2026-10-11", "Asia/Beirut"))
	if len(report.Commits) != 1 || report.Days["2026-10-11"] != 1 {
		t.Fatalf("author date, timezone, and day total disagree: %+v", report)
	}
	if report.Commits[0].Message != "recent author old committer" || !report.Commits[0].CommitterWhen.Equal(old) {
		t.Fatal("wrong commit selected or metadata lost")
	}
}

func TestDayBucketsAcrossDST(t *testing.T) {
	for _, zone := range []string{"Europe/Berlin", "Asia/Beirut"} {
		t.Run(zone, func(t *testing.T) {
			location := testLocation(t, zone)
			path := t.TempDir()
			repo := createTestRepo(t, path, "tester@test.com", 0, testClock)
			for day := 28; day <= 30; day++ {
				when := time.Date(2026, 3, day, 12, 0, 0, 0, location)
				addTestCommit(t, repo, when.String(), "tester@test.com", when, when)
			}
			// Beirut advances its clock at midnight: midnight itself does not exist.
			when := time.Date(2026, 3, 29, 1, 0, 0, 0, location)
			addTestCommit(t, repo, "first hour after midnight change", "tester@test.com", when, when)
			q := testQuery(t, "2026-03-28", "2026-03-30", zone)
			report := queryRepos(t, []string{path}, q)
			want := map[string]int{"2026-03-28": 1, "2026-03-29": 2, "2026-03-30": 1}
			if !reflect.DeepEqual(report.Days, want) {
				t.Fatalf("got %v, want %v", report.Days, want)
			}
			q = testQuery(t, "2026-03-29", "2026-03-29", zone)
			report = queryRepos(t, []string{path}, q)
			if len(report.Commits) != 2 {
				t.Fatalf("single DST day: %+v", report)
			}
		})
	}
}

func TestAuthorFiltersAndFutureCommits(t *testing.T) {
	path := t.TempDir()
	repo := createTestRepo(t, path, "first@test.com", 1, testClock.Add(-time.Hour))
	addTestCommit(t, repo, "second author", "second@test.com", testClock.Add(-time.Hour), testClock)
	addTestCommit(t, repo, "later today", "first@test.com", testClock.Add(time.Hour), testClock)
	q := testQuery(t, "2026-10-16", "2026-10-16", "UTC")
	for _, tt := range []struct {
		emails []string
		count  int
	}{
		{[]string{"first@test.com"}, 1},
		{[]string{"first@test.com", "second@test.com"}, 2},
		{[]string{"*"}, 2},
		{[]string{"missing@test.com"}, 0},
	} {
		q.Emails = tt.emails
		report := queryRepos(t, []string{path}, q)
		if len(report.Commits) != tt.count {
			t.Fatalf("authors %v: got %d, want %d", tt.emails, len(report.Commits), tt.count)
		}
	}
}

func TestBranchScope(t *testing.T) {
	path := t.TempDir()
	when := mustTime(t, "2026-10-10T12:00:00Z")
	repo := createTestRepo(t, path, "tester@test.com", 1, when)
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	main := head.Name()
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	feature := plumbing.NewBranchReferenceName("feature")
	if err := worktree.Checkout(&git.CheckoutOptions{Branch: feature, Create: true}); err != nil {
		t.Fatal(err)
	}
	addTestCommit(t, repo, "unmerged feature", "tester@test.com", when, when)
	hidden := plumbing.NewBranchReferenceName("hidden")
	if err := worktree.Checkout(&git.CheckoutOptions{Branch: hidden, Create: true}); err != nil {
		t.Fatal(err)
	}
	hiddenHash := addTestCommit(t, repo, "remote tag and stash only", "tester@test.com", when, when)
	if err := worktree.Checkout(&git.CheckoutOptions{Branch: main}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.RemoveReference(hidden); err != nil {
		t.Fatal(err)
	}
	for _, name := range []plumbing.ReferenceName{"refs/remotes/origin/hidden", "refs/tags/hidden", "refs/stash"} {
		if err := repo.Storer.SetReference(plumbing.NewHashReference(name, hiddenHash)); err != nil {
			t.Fatal(err)
		}
	}
	q := testQuery(t, "2026-10-10", "2026-10-10", "UTC")
	check := func(want int) {
		t.Helper()
		report := queryRepos(t, []string{path}, q)
		if len(report.Commits) != want {
			t.Fatalf("scope %s: got %d, want %d", q.BranchScope, len(report.Commits), want)
		}
	}
	check(2) // Shared ancestors count once, and other refs are not local branches.
	q.BranchScope = HeadBranch
	check(1)
	if err := worktree.Checkout(&git.CheckoutOptions{Branch: feature}); err != nil {
		t.Fatal(err)
	}
	q.BranchScope = LocalBranches
	check(2) // Default total is stable across checkouts.
	q.BranchScope = HeadBranch
	check(2)
	if err := worktree.Checkout(&git.CheckoutOptions{Hash: hiddenHash}); err != nil {
		t.Fatal(err)
	}
	q.BranchScope = LocalBranches
	check(3) // A detached HEAD is also included.
}

func TestDuplicateClonesRetainMembership(t *testing.T) {
	source, clone := t.TempDir(), t.TempDir()
	createTestRepo(t, source, "tester@test.com", 3, testClock.Add(-time.Hour))
	if _, err := git.PlainClone(clone, false, &git.CloneOptions{URL: source}); err != nil {
		t.Fatal(err)
	}
	report := queryRepos(t, []string{source, clone}, testQuery(t, "", "", "UTC"))
	if report.Processed != 2 || len(report.Commits) != 3 || report.Days["2026-10-16"] != 3 {
		t.Fatalf("duplicate clone inflated total: %+v", report)
	}
	for _, commit := range report.Commits {
		if len(commit.Repositories) != 2 {
			t.Fatalf("lost clone membership: %+v", commit)
		}
	}
}

func TestLinkedWorktree(t *testing.T) {
	source := t.TempDir()
	linked := filepath.Join(t.TempDir(), "linked")
	createTestRepo(t, source, "tester@test.com", 2, testClock.Add(-time.Hour))
	gitCommand(t, source, "worktree", "add", "--detach", linked, "HEAD")
	scan, err := scanGitFolders(context.Background(), linked)
	if err != nil || len(scan.Repos) != 1 {
		t.Fatalf("worktree discovery: %+v, %v", scan, err)
	}
	report := queryRepos(t, scan.Repos, testQuery(t, "", "", "UTC"))
	if len(report.Diagnostics) != 0 || len(report.Commits) != 2 {
		t.Fatalf("worktree history: %+v", report)
	}
	report = queryRepos(t, []string{source, linked}, testQuery(t, "", "", "UTC"))
	if len(report.Commits) != 2 || len(report.Commits[0].Repositories) != 2 {
		t.Fatalf("worktree duplicate: %+v", report)
	}
}

func TestInitializedSubmodule(t *testing.T) {
	parent, source := t.TempDir(), t.TempDir()
	createTestRepo(t, parent, "tester@test.com", 0, testClock)
	createTestRepo(t, source, "tester@test.com", 2, testClock.Add(-time.Hour))
	gitCommand(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", source, "module")
	scan, err := scanGitFolders(context.Background(), parent)
	if err != nil || len(scan.Repos) != 2 {
		t.Fatalf("submodule discovery: %+v, %v", scan, err)
	}
	report := queryRepos(t, scan.Repos, testQuery(t, "", "", "UTC"))
	if report.Processed != 2 || len(report.Diagnostics) != 0 || len(report.Commits) != 2 {
		t.Fatalf("submodule history: %+v", report)
	}
}

func TestShallowHistory(t *testing.T) {
	path := t.TempDir()
	repo := createTestRepo(t, path, "tester@test.com", 3, testClock.Add(-time.Hour))
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetShallow([]plumbing.Hash{head.Hash()}); err != nil {
		t.Fatal(err)
	}
	report := queryRepos(t, []string{path}, testQuery(t, "", "", "UTC"))
	if len(report.Diagnostics) != 0 || len(report.Commits) != 1 {
		t.Fatalf("shallow boundary ignored: %+v", report)
	}
}

func TestRepositoryFailuresAndEmptyRepos(t *testing.T) {
	good, empty, broken := t.TempDir(), t.TempDir(), t.TempDir()
	createTestRepo(t, good, "tester@test.com", 3, testClock.Add(-time.Hour))
	createTestRepo(t, empty, "tester@test.com", 0, testClock)
	if err := os.Mkdir(filepath.Join(broken, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	report := queryRepos(t, []string{good, broken, empty}, testQuery(t, "", "", "UTC"))
	if report.Processed != 2 || len(report.Commits) != 3 || len(report.Diagnostics) != 1 || report.Diagnostics[0].Path != broken {
		t.Fatalf("partial results were lost or empty repo treated as failure: %+v", report)
	}
}

func TestCancellationAndNoRepos(t *testing.T) {
	q := testQuery(t, "", "", "UTC")
	report := queryRepos(t, nil, q)
	if len(report.Commits) != 0 || report.Processed != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := processRepos(ctx, []string{t.TempDir()}, q); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was swallowed: %v", err)
	}
}
