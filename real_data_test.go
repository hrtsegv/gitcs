package main

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
)

// Opt in to a read-only check of real repositories; portable failure fixtures
// remain part of the normal suite. Native Git supplies an independent oracle.
func TestRealDataAgainstNativeGit(t *testing.T) {
	root := os.Getenv("GITCS_TEST_REAL_ROOT")
	if root == "" {
		t.Skip("set GITCS_TEST_REAL_ROOT to check a real repository directory")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("native Git is required for the real-data audit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	now := time.Now()
	scan, err := scanGitFolders(ctx, root)
	if err != nil || len(scan.Diagnostics) != 0 || len(scan.Repos) == 0 {
		t.Fatalf("real-data scan must be readable and nonempty: %d repos, %v, %v", len(scan.Repos), scan.Diagnostics, err)
	}
	oracles := make(map[BranchScope]map[plumbing.Hash]CommitRecord)
	for _, scope := range []BranchScope{LocalBranches, HeadBranch} {
		oracles[scope] = nativeRealCommits(t, ctx, scan.Repos, scope)
	}
	authors := make(map[string]int)
	windowStart := calendarDate(now.UTC()).AddDate(0, 0, -sixMonthsInDays).Format(dateLayout)
	for _, commit := range oracles[LocalBranches] {
		if commit.AuthorWhen.After(now) || commit.AuthorWhen.UTC().Format(dateLayout) < windowStart {
			continue
		}
		if _, err := parseEmails([]string{commit.AuthorEmail}); err == nil && commit.AuthorEmail != "*" {
			authors[commit.AuthorEmail]++
		}
	}
	emails := slices.Collect(maps.Keys(authors))
	slices.SortFunc(emails, func(a, b string) int {
		if authors[a] != authors[b] {
			return authors[b] - authors[a]
		}
		return strings.Compare(a, b)
	})
	today := now.In(testLocation(t, "Asia/Beirut")).Format(dateLayout)
	type auditCase struct {
		name, zone, since, until string
		scope                    BranchScope
		emails                   []string
	}
	cases := []auditCase{
		{"local UTC", "UTC", "", "", LocalBranches, []string{"*"}},
		{"HEAD UTC", "UTC", "", "", HeadBranch, []string{"*"}},
		{"local Beirut", "Asia/Beirut", "", "", LocalBranches, []string{"*"}},
		{"local Los Angeles", "America/Los_Angeles", "", "", LocalBranches, []string{"*"}},
		{"single day", "Asia/Beirut", today, today, LocalBranches, []string{"*"}},
		{"empty historical range", "UTC", "1900-01-01", "1900-01-02", LocalBranches, []string{"*"}},
	}
	if len(emails) > 0 {
		cases = append(cases, auditCase{"one author", "Asia/Beirut", "", "", LocalBranches, emails[:1]})
	}
	if len(emails) > 1 {
		cases = append(cases, auditCase{"multiple authors", "UTC", "", "", LocalBranches, emails[:2]})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			boundary, err := setTimeFlags(tc.since, tc.until, now.In(testLocation(t, tc.zone)))
			if err != nil {
				t.Fatal(err)
			}
			q := Query{Boundary: boundary, Emails: tc.emails, BranchScope: tc.scope, Now: now}
			want, days := make(map[plumbing.Hash]bool), make(map[string]int)
			for hash, commit := range oracles[tc.scope] {
				date := commit.AuthorWhen.In(boundary.Location).Format(dateLayout)
				if date < boundary.Since.Format(dateLayout) || date >= boundary.Until.Format(dateLayout) || commit.AuthorWhen.After(now) {
					continue
				}
				if !slices.Contains(tc.emails, "*") && !slices.Contains(tc.emails, commit.AuthorEmail) {
					continue
				}
				want[hash], days[date] = true, days[date]+1
			}
			report, err := processRepos(ctx, scan.Repos, q)
			if err != nil || len(report.Diagnostics) != 0 || report.Processed != len(scan.Repos) {
				t.Fatalf("report: %v, %v, processed=%d", err, report.Diagnostics, report.Processed)
			}
			assertExplorerCountHashes(t, report.Commits, want)
			if !maps.Equal(report.Days, days) {
				t.Fatalf("report daily counts differ from native Git: got=%v want=%v", report.Days, days)
			}
			m := newExplorer(ctx, root, q, 0)
			defer m.cancel()
			for cmd := m.Init(); cmd != nil; {
				_, cmd = m.Update(cmd())
			}
			assertExplorerCountHashes(t, m.builder.report.Commits, want)
			if m.loading || m.scanErr != nil || len(m.builder.report.Diagnostics) != 0 || !maps.Equal(m.days, days) {
				t.Fatalf("explorer differs from native Git: %s; days=%v want=%v", m.statusLine(), m.days, days)
			}
			args := []string{"-path", root, "-since", boundary.Since.Format(dateLayout), "-until", boundary.Until.AddDate(0, 0, -1).Format(dateLayout), "-timezone", tc.zone, "-branch-scope", string(tc.scope), "-width", "40"}
			for _, email := range tc.emails {
				args = append(args, "-email", email)
			}
			var out, errOut bytes.Buffer
			if code := run(ctx, args, &out, &errOut, now); code != 0 || errOut.Len() != 0 || !strings.Contains(plain(out.String()), fmt.Sprintf("Total commits: %d between", len(want))) {
				t.Fatalf("printable CLI differs from native Git: exit=%d stderr=%s", code, errOut.String())
			}
			t.Logf("%d repositories, %d unique commits, %d active dates: native Git, report, CLI and explorer agree", len(scan.Repos), len(want), len(days))
		})
	}
}

func nativeRealCommits(t *testing.T, ctx context.Context, paths []string, scope BranchScope) map[plumbing.Hash]CommitRecord {
	t.Helper()
	result := make(map[plumbing.Hash]CommitRecord)
	for _, path := range paths {
		git := func(args ...string) ([]byte, error) {
			command := exec.CommandContext(ctx, "git", append([]string{"--no-pager", "--no-replace-objects", "-C", path, "-c", "log.showSignature=false"}, args...)...)
			command.Env = patchEnvironment(os.Environ())
			return command.Output()
		}
		var roots []string
		if scope == LocalBranches {
			out, err := git("for-each-ref", "--format=%(objectname)", "refs/heads")
			if err != nil {
				t.Fatalf("native Git refs in %q: %v", path, err)
			}
			roots = strings.Fields(string(out))
		}
		if head, err := git("rev-parse", "--verify", "HEAD"); err == nil {
			roots = append(roots, strings.TrimSpace(string(head)))
		}
		if len(roots) == 0 {
			continue
		}
		args := append([]string{"log", "--format=%H%x00%at%x00%ae%x00"}, roots...)
		out, err := git(append(args, "--")...)
		if err != nil {
			t.Fatalf("native Git history in %q: %v", path, err)
		}
		fields := strings.Split(string(out), "\x00")
		for i := 0; i+2 < len(fields); i += 3 {
			hash := plumbing.NewHash(strings.TrimSpace(fields[i]))
			stamp, err := strconv.ParseInt(fields[i+1], 10, 64)
			if err != nil || hash == plumbing.ZeroHash {
				t.Fatalf("invalid native Git metadata in %q", path)
			}
			result[hash] = CommitRecord{Hash: hash, AuthorWhen: time.Unix(stamp, 0), AuthorEmail: fields[i+2]}
		}
	}
	return result
}
