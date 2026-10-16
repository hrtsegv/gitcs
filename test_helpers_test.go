package main

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

var testClock = time.Date(2026, 10, 16, 18, 0, 0, 0, time.UTC)
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func plain(s string) string { return ansiPattern.ReplaceAllString(s, "") }

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	when, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return when
}

func testLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func testQuery(t *testing.T, since, until, timezone string) Query {
	t.Helper()
	now := testClock.In(testLocation(t, timezone))
	b, err := setTimeFlags(since, until, now)
	if err != nil {
		t.Fatal(err)
	}
	return Query{Boundary: b, Emails: []string{"tester@test.com"}, BranchScope: LocalBranches, Now: now}
}

func addTestCommit(t *testing.T, repo *git.Repository, message, email string, author, committer time.Time) plumbing.Hash {
	t.Helper()
	w, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := w.Commit(message, &git.CommitOptions{
		AllowEmptyCommits: true,
		Author:            &object.Signature{Name: "Tester", Email: email, When: author},
		Committer:         &object.Signature{Name: "Committer", Email: email, When: committer},
	})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func createTestRepo(t *testing.T, path, email string, count int, when time.Time) *git.Repository {
	t.Helper()
	repo, err := git.PlainInit(path, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := range count {
		addTestCommit(t, repo, fmt.Sprintf("%s commit %d", path, i), email, when, when)
	}
	return repo
}

func gitCommand(t *testing.T, path string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("native Git is required for this integration test")
	}
	command := exec.Command("git", append([]string{"-C", path}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func queryRepos(t *testing.T, repos []string, q Query) Report {
	t.Helper()
	report, err := processRepos(context.Background(), repos, q)
	if err != nil {
		t.Fatal(err)
	}
	return report
}
