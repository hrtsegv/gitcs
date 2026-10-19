package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

type explorerCountFixture struct {
	root, source, clone, independent string
	hashes                           map[string]plumbing.Hash
}

// Keep independent expected hashes and civil-day counts below: comparing the
// two commands alone would miss a counting error in their shared reader.
func newExplorerCountFixture(t *testing.T) explorerCountFixture {
	t.Helper()
	f := explorerCountFixture{root: t.TempDir(), hashes: make(map[string]plumbing.Hash)}
	f.source = filepath.Join(f.root, "source")
	f.clone = filepath.Join(f.root, "clone")
	f.independent = filepath.Join(f.root, "independent")
	repo := createTestRepo(t, f.source, "tester@test.com", 0, testClock)
	defer repo.Storer.(io.Closer).Close()
	add := func(repo *git.Repository, name, message, email, stamp string) {
		t.Helper()
		f.hashes[name] = addTestCommit(t, repo, message, email, mustTime(t, stamp), testClock)
	}
	// Beirut is UTC+3 on these dates. These adjacent timestamps straddle
	// both ends of the single-day range without relying on committer dates.
	add(repo, "before", "before range", "tester@test.com", "2026-10-09T20:59:59Z")
	add(repo, "first", "first included instant", "tester@test.com", "2026-10-09T21:00:00Z")
	add(repo, "last", "last included instant", "tester@test.com", "2026-10-10T20:59:59Z")
	add(repo, "second", "second author", "second@test.com", "2026-10-10T21:00:00Z")
	add(repo, "outside", "after range", "tester@test.com", "2026-10-11T21:00:00Z")
	add(repo, "future", "future author date", "tester@test.com", "2026-10-16T18:00:01Z")

	clone, err := git.PlainClone(f.clone, false, &git.CloneOptions{URL: f.source})
	if err != nil {
		t.Fatal(err)
	}
	defer clone.Storer.(io.Closer).Close()
	add(clone, "clone", "clone needle", "tester@test.com", "2026-10-11T09:00:00Z")

	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	w, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("feature"), Create: true}); err != nil {
		t.Fatal(err)
	}
	add(repo, "feature", "feature needle", "tester@test.com", "2026-10-10T21:30:00Z")
	if err := w.Checkout(&git.CheckoutOptions{Branch: head.Name()}); err != nil {
		t.Fatal(err)
	}
	// An older author date after the future-dated ancestor also verifies that
	// neither command prunes history based on traversal order.
	add(repo, "main", "main only", "tester@test.com", "2026-10-11T10:00:00Z")
	other := createTestRepo(t, f.independent, "second@test.com", 0, testClock)
	defer other.Storer.(io.Closer).Close()
	add(other, "independent", "independent needle", "second@test.com", "2026-10-10T09:00:00Z")
	f.source, f.clone, f.independent = resolvedPath(t, f.source), resolvedPath(t, f.clone), resolvedPath(t, f.independent)
	return f
}

func TestExplorerReportCountParity(t *testing.T) {
	f := newExplorerCountFixture(t)
	for _, tt := range []struct {
		name, zone, since, until string
		emails                   []string
		scope                    BranchScope
		names                    []string
		days                     map[string]int
	}{
		{"all authors", "Asia/Beirut", "2026-10-10", "2026-10-11", []string{"*"}, LocalBranches,
			[]string{"first", "last", "second", "feature", "main", "clone", "independent"}, map[string]int{"2026-10-10": 3, "2026-10-11": 4}},
		{"one author", "Asia/Beirut", "2026-10-10", "2026-10-11", []string{"tester@test.com"}, LocalBranches,
			[]string{"first", "last", "feature", "main", "clone"}, map[string]int{"2026-10-10": 2, "2026-10-11": 3}},
		{"multiple authors", "Asia/Beirut", "2026-10-10", "2026-10-11", []string{"tester@test.com", "second@test.com"}, LocalBranches,
			[]string{"first", "last", "second", "feature", "main", "clone", "independent"}, map[string]int{"2026-10-10": 3, "2026-10-11": 4}},
		{"HEAD only", "Asia/Beirut", "2026-10-10", "2026-10-11", []string{"*"}, HeadBranch,
			[]string{"first", "last", "second", "main", "clone", "independent"}, map[string]int{"2026-10-10": 3, "2026-10-11": 3}},
		{"UTC boundary", "UTC", "2026-10-10", "2026-10-10", []string{"*"}, LocalBranches,
			[]string{"last", "second", "feature", "independent"}, map[string]int{"2026-10-10": 4}},
		{"Beirut boundary", "Asia/Beirut", "2026-10-10", "2026-10-10", []string{"*"}, LocalBranches,
			[]string{"first", "last", "independent"}, map[string]int{"2026-10-10": 3}},
		{"future exclusion", "UTC", "2026-10-09", "2026-10-16", []string{"*"}, LocalBranches,
			[]string{"before", "first", "last", "second", "outside", "feature", "main", "clone", "independent"}, map[string]int{"2026-10-09": 2, "2026-10-10": 4, "2026-10-11": 3}},
		{"no matching author", "UTC", "2026-10-09", "2026-10-16", []string{"missing@test.com"}, LocalBranches,
			nil, map[string]int{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			q := testQuery(t, tt.since, tt.until, tt.zone)
			q.Emails, q.BranchScope = tt.emails, tt.scope
			scan, err := scanGitFolders(context.Background(), f.root)
			if err != nil || len(scan.Diagnostics) != 0 || len(scan.Repos) != 3 {
				t.Fatalf("scan: %+v, %v", scan, err)
			}
			report := queryRepos(t, scan.Repos, q)
			want := make(map[plumbing.Hash]bool, len(tt.names))
			for _, name := range tt.names {
				want[f.hashes[name]] = true
			}
			assertExplorerCountHashes(t, report.Commits, want)
			if !maps.Equal(report.Days, tt.days) || len(report.Diagnostics) != 0 || report.Processed != 3 {
				t.Fatalf("normal report: days=%v, want=%v; processed=%d, diagnostics=%v", report.Days, tt.days, report.Processed, report.Diagnostics)
			}

			m := newExplorer(context.Background(), f.root, q, 0)
			t.Cleanup(m.cancel)
			m.color = false
			for cmd := m.Init(); cmd != nil; {
				_, cmd = m.Update(cmd())
				assertExplorerUnfilteredCounts(t, m)
			}
			if m.loading || m.discovery || m.completed != 3 || m.builder.report.Processed != 3 {
				t.Fatalf("explorer did not complete all repositories: %s", m.statusLine())
			}
			assertExplorerCountHashes(t, m.builder.report.Commits, want)
			if !maps.Equal(m.days, report.Days) {
				t.Fatalf("explorer days=%v, report days=%v", m.days, report.Days)
			}
			// Shared hashes retain the same memberships regardless of which
			// repository's asynchronous result was consumed first.
			for _, normal := range report.Commits {
				for _, interactive := range m.builder.report.Commits {
					if normal.Hash == interactive.Hash {
						paths := slices.Clone(interactive.Repositories)
						slices.Sort(paths)
						if !slices.Equal(paths, normal.Repositories) {
							t.Fatalf("membership differs for %s: %v, %v", normal.Hash, paths, normal.Repositories)
						}
					}
				}
			}
			m.autoDate = false
			for day := q.Boundary.Since; day.Before(q.Boundary.Until); day = day.AddDate(0, 0, 1) {
				m.date = day
				m.refreshSelection(false)
				if len(m.visible) != report.Days[day.Format(dateLayout)] {
					t.Fatalf("%s: list=%d, report=%d", day.Format(dateLayout), len(m.visible), report.Days[day.Format(dateLayout)])
				}
			}
			press(m, 'd')
			if len(m.visible) != len(report.Commits) {
				t.Fatalf("all dates list=%d, report total=%d", len(m.visible), len(report.Commits))
			}

			// Exercise the real printable command with the same flags too.
			args := []string{"-path", f.root, "-since", tt.since, "-until", tt.until, "-timezone", tt.zone, "-branch-scope", string(tt.scope)}
			for _, email := range tt.emails {
				args = append(args, "-email", email)
			}
			var out, errOut bytes.Buffer
			code := run(context.Background(), args, &out, &errOut, testClock)
			if code != 0 || errOut.Len() != 0 || !strings.Contains(plain(out.String()), fmt.Sprintf("Total commits: %d between", len(want))) {
				t.Fatalf("normal command: code=%d, stdout=%s, stderr=%s", code, out.String(), errOut.String())
			}
		})
	}
}

func TestExplorerFilteredCountsAsCloneArrives(t *testing.T) {
	f := newExplorerCountFixture(t)
	q := testQuery(t, "2026-10-10", "2026-10-11", "Asia/Beirut")
	q.Emails = []string{"*"}
	m := newExplorer(context.Background(), f.root, q, 0)
	t.Cleanup(m.cancel)
	m.discovery = false
	m.repos = []string{f.source, f.clone, f.independent}
	m.repo = f.clone
	m.search.SetValue("needle")
	for step, path := range []string{f.source, f.independent, f.clone} {
		commits, err := readRepo(context.Background(), path, q)
		if err != nil {
			t.Fatal(err)
		}
		m.Update(repositoryMsg{generation: m.generation, result: repositoryResult{path: path, commits: commits}})
		if len(m.builder.report.Commits) != 5+step {
			t.Fatalf("step %d: global unique total=%d, want=%d", step, len(m.builder.report.Commits), 5+step)
		}
		want := 0
		if step == 2 {
			want = 1
		}
		if len(m.visible) != want || sumExplorerDays(m.days) != want {
			t.Fatalf("step %d: matching list=%d, days=%v, want=%d", step, len(m.visible), m.days, want)
		}
	}
	m.Update(repositoryMsg{generation: m.generation, done: true})
	for _, tt := range []struct {
		name, repo, search string
		names              []string
		days               map[string]int
	}{
		{"message search", "", "NEEDLE", []string{"feature", "clone", "independent"}, map[string]int{"2026-10-10": 1, "2026-10-11": 2}},
		{"source membership", f.source, "", []string{"first", "last", "second", "feature", "main"}, map[string]int{"2026-10-10": 2, "2026-10-11": 3}},
		{"clone membership", f.clone, "", []string{"first", "last", "second", "clone"}, map[string]int{"2026-10-10": 2, "2026-10-11": 2}},
		{"combined filter", f.clone, "needle", []string{"clone"}, map[string]int{"2026-10-11": 1}},
		{"hash search", "", f.hashes["first"].String(), []string{"first"}, map[string]int{"2026-10-10": 1}},
		{"no matches", "", "no such message", nil, map[string]int{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m.repo, m.allDates = tt.repo, true
			m.search.SetValue(tt.search)
			m.refreshSelection(false)
			var records []CommitRecord
			for _, index := range m.visible {
				records = append(records, m.builder.report.Commits[index])
			}
			want := make(map[plumbing.Hash]bool, len(tt.names))
			for _, name := range tt.names {
				want[f.hashes[name]] = true
			}
			assertExplorerCountHashes(t, records, want)
			if !maps.Equal(m.days, tt.days) || sumExplorerDays(m.days) != len(records) {
				t.Fatalf("filtered days=%v, want=%v; list=%d", m.days, tt.days, len(records))
			}
			if len(m.builder.report.Commits) != 7 || sumExplorerDays(m.builder.report.Days) != 7 {
				t.Fatal("interactive filtering changed the underlying report total")
			}
		})
	}
}

func assertExplorerCountHashes(t *testing.T, commits []CommitRecord, want map[plumbing.Hash]bool) {
	t.Helper()
	seen := make(map[plumbing.Hash]bool, len(commits))
	for _, commit := range commits {
		if seen[commit.Hash] || !want[commit.Hash] {
			t.Fatalf("duplicate or unexpected commit %s: %s", commit.Hash, commit.Message)
		}
		seen[commit.Hash] = true
	}
	if !maps.Equal(seen, want) {
		t.Fatalf("hash count=%d, want=%d; got=%v, want=%v", len(seen), len(want), seen, want)
	}
}

func assertExplorerUnfilteredCounts(t *testing.T, m *explorer) {
	t.Helper()
	if !maps.Equal(m.days, m.builder.report.Days) || sumExplorerDays(m.days) != len(m.builder.report.Commits) {
		t.Fatalf("incremental unique totals differ: explorer=%v, report=%v, commits=%d", m.days, m.builder.report.Days, len(m.builder.report.Commits))
	}
	day := m.date.Format(dateLayout)
	if len(m.visible) != m.days[day] {
		t.Fatalf("incremental selected day %s: list=%d, calendar=%d", day, len(m.visible), m.days[day])
	}
}

func sumExplorerDays(days map[string]int) int {
	total := 0
	for _, count := range days {
		total += count
	}
	return total
}
