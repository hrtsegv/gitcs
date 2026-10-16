package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runForTest(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	args = append([]string{"-timezone", "UTC", "-width", "80"}, args...)
	code := run(context.Background(), args, &out, &errOut, testClock)
	return code, plain(out.String()), plain(errOut.String())
}

func isolateGitConfig(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "globalconfig"))
	t.Setenv("GIT_CONFIG_COUNT", "0")
}

func TestRunDefaultPathAndLocalEmail(t *testing.T) {
	isolateGitConfig(t)
	path := t.TempDir()
	createTestRepo(t, path, "tester@test.com", 3, testClock)
	gitCommand(t, path, "config", "user.email", "tester@test.com")
	t.Chdir(path)
	code, out, errOut := runForTest()
	if code != 0 || !strings.Contains(out, "Total commits: 3") || errOut != "" {
		t.Fatalf("default invocation: code=%d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "all local branches + HEAD") || !strings.Contains(out, "Dates: author | Timezone: UTC") {
		t.Fatalf("missing visible scope: %s", out)
	}
}

func TestRunMailboxAndMultipleEmails(t *testing.T) {
	path := t.TempDir()
	repo := createTestRepo(t, path, "tester@test.com", 1, testClock)
	addTestCommit(t, repo, "second identity", "work@test.com", testClock, testClock)
	for _, tt := range []struct {
		emails []string
		count  string
	}{
		{[]string{"Tester <tester@test.com>"}, "1"},
		{[]string{"tester@test.com", "work@test.com"}, "2"},
		{[]string{"*"}, "2"},
	} {
		args := []string{"-path", path, "-since", "2026-10-16", "-until", "2026-10-16"}
		for _, email := range tt.emails {
			args = append(args, "-email", email)
		}
		code, out, errOut := runForTest(args...)
		if code != 0 || errOut != "" || !strings.Contains(out, "Total commits: "+tt.count) {
			t.Fatalf("emails %v: code=%d\n%s\n%s", tt.emails, code, out, errOut)
		}
	}
}

func TestRunErrorsAndHelp(t *testing.T) {
	isolateGitConfig(t)
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		args    []string
		message string
		code    int
	}{
		{"missing directory", []string{"-path", filepath.Join(root, "missing")}, "cannot scan", 1},
		{"file", []string{"-path", file}, "not a directory", 1},
		{"no email", []string{"-path", root}, "no Git email configured", 1},
		{"invalid email", []string{"-path", root, "-email", "invalid"}, "invalid email", 1},
		{"reversed dates", []string{"-since", "2026-10-12", "-until", "2026-10-10"}, "must be on or before", 1},
		{"bad date", []string{"-since", "10/10/2026"}, "YYYY-MM-DD", 1},
		{"bad scope", []string{"-branch-scope", "all"}, "use local or head", 1},
		{"bad zone", []string{"-timezone", "Unknown/Zone"}, "unknown timezone", 1},
		{"bad width", []string{"-width", "5"}, "at least 20", 1},
		{"positional args", []string{"unused"}, "use -path", 1},
		{"unknown flag", []string{"-unknown"}, "flag provided but not defined", 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, _, errOut := runForTest(tt.args...)
			if code != tt.code || !strings.Contains(errOut, tt.message) {
				t.Fatalf("code %d, stderr %s", code, errOut)
			}
		})
	}
	code, _, help := runForTest("-help")
	if code != 0 || !strings.Contains(help, "'*'") || !strings.Contains(help, "YYYY-MM-DD") || !strings.Contains(help, "directory to scan") {
		t.Fatalf("help code %d: %s", code, help)
	}
}

func TestRunPartialAndFailedResults(t *testing.T) {
	for _, directory := range []string{"broken", "broken with spaces", `broken\repo`} {
		t.Run(directory, func(t *testing.T) {
			root := t.TempDir()
			broken := filepath.Join(root, directory)
			if err := os.MkdirAll(filepath.Join(broken, ".git"), 0755); err != nil {
				t.Fatal(err)
			}
			expectedWarning := fmt.Sprintf("gitcs: skipped %q:", resolvedPath(t, broken))
			code, out, errOut := runForTest("-path", broken, "-email", "*")
			if code == 0 || strings.Contains(out, "Total commits: 0") {
				t.Fatalf("total failure looked successful: %d\n%s\n%s", code, out, errOut)
			}
			if !strings.Contains(errOut, expectedWarning) {
				t.Fatalf("missing repository warning %q in stderr:\n%s", expectedWarning, errOut)
			}
			createTestRepo(t, filepath.Join(root, "good"), "tester@test.com", 1, testClock)
			code, out, errOut = runForTest("-path", root, "-email", "*")
			if code != 0 || !strings.Contains(out, "Partial results") || !strings.Contains(out, "Total commits: 1") {
				t.Fatalf("partial result: %d\n%s\n%s", code, out, errOut)
			}
			if !strings.Contains(errOut, expectedWarning) {
				t.Fatalf("missing repository warning %q in stderr:\n%s", expectedWarning, errOut)
			}
		})
	}
}

func TestRunEmptyStates(t *testing.T) {
	root := t.TempDir()
	code, out, errOut := runForTest("-path", root, "-email", "*")
	if code != 0 || errOut != "" || !strings.Contains(out, "No Git repositories found") {
		t.Fatalf("empty root: %d\n%s\n%s", code, out, errOut)
	}
	createTestRepo(t, root, "tester@test.com", 1, testClock)
	code, out, errOut = runForTest("-path", root, "-email", "missing@test.com")
	if code != 0 || errOut != "" || !strings.Contains(out, "No commits matched") || !strings.Contains(out, "Total commits: 0") {
		t.Fatalf("empty filter: %d\n%s\n%s", code, out, errOut)
	}
}

func TestRunFutureEndAndCancellation(t *testing.T) {
	path := t.TempDir()
	createTestRepo(t, path, "tester@test.com", 1, testClock)
	code, out, errOut := runForTest("-path", path, "-email", "*", "-until", "2099-01-01")
	if code != 0 || !strings.Contains(errOut, "using 2026-10-16") || !strings.Contains(out, "and 2026-10-16") {
		t.Fatalf("future end: %d\n%s\n%s", code, out, errOut)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	code = run(ctx, []string{"-path", path, "-email", "*"}, &stdout, &stderr, testClock)
	if code != 130 || !strings.Contains(stderr.String(), "canceled") || stdout.Len() != 0 {
		t.Fatalf("canceled scan: %d\n%s\n%s", code, &stdout, &stderr)
	}
}
