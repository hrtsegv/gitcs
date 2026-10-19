package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGitConfigPrecedenceAndConditionalInclude(t *testing.T) {
	isolateGitConfig(t)
	root := t.TempDir()
	globalRepo := filepath.Join(root, "global")
	createTestRepo(t, globalRepo, "global@test.com", 1, testClock)
	conditionalRepo := filepath.Join(root, "conditional")
	repo := createTestRepo(t, conditionalRepo, "included@test.com", 2, testClock)
	addTestCommit(t, repo, "local identity", "local@test.com", testClock, testClock)
	globalConfig := os.Getenv("GIT_CONFIG_GLOBAL")
	includeConfig := filepath.Join(root, "identity config")
	gitCommand(t, root, "config", "--file", globalConfig, "user.email", "global@test.com")
	gitCommand(t, root, "config", "--file", includeConfig, "user.email", "included@test.com")
	condition := fmt.Sprintf("includeIf.gitdir/i:%s/.git.path", filepath.ToSlash(resolvedPath(t, conditionalRepo)))
	gitCommand(t, root, "config", "--file", globalConfig, condition, includeConfig)

	check := func(path, email string, count int, extra ...string) {
		t.Helper()
		args := append([]string{"-path", path}, extra...)
		code, out, errOut := runForTest(args...)
		if code != 0 || errOut != "" || !strings.Contains(out, "Authors: "+email+"\n") || !strings.Contains(out, fmt.Sprintf("Total commits: %d between", count)) {
			t.Fatalf("Git configuration identity: code=%d\n%s\n%s", code, out, errOut)
		}
	}
	check(globalRepo, "global@test.com", 1)
	check(conditionalRepo, "included@test.com", 2)
	gitCommand(t, conditionalRepo, "config", "user.email", "local@test.com")
	check(conditionalRepo, "local@test.com", 1)
	check(conditionalRepo, "included@test.com", 2, "-email", "included@test.com")
}

func TestRunWithoutNativeGit(t *testing.T) {
	isolateGitConfig(t)
	root := t.TempDir()
	createTestRepo(t, root, "tester@test.com", 2, testClock)
	t.Setenv("PATH", t.TempDir())
	code, _, errOut := runForTest("-path", root)
	if code != 1 || !strings.Contains(errOut, "use -email") {
		t.Fatalf("missing Git should explain explicit authors: code=%d stderr=%s", code, errOut)
	}
	for _, email := range []string{"tester@test.com", "*"} {
		code, out, errOut := runForTest("-path", root, "-email", email)
		if code != 0 || errOut != "" || !strings.Contains(out, "Total commits: 2 between") {
			t.Fatalf("explicit authors should work without native Git: code=%d\n%s\n%s", code, out, errOut)
		}
	}
}

func TestRunCanceledDefaultAuthor(t *testing.T) {
	isolateGitConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer
	code := run(ctx, []string{"-path", t.TempDir()}, &out, &errOut, testClock)
	if code != 130 || out.Len() != 0 || !strings.Contains(errOut.String(), "context canceled") {
		t.Fatalf("default author masked cancellation: code=%d\n%s\n%s", code, &out, &errOut)
	}
}

func TestRunAdditionalFlagFailures(t *testing.T) {
	for _, tt := range []struct {
		name, message string
		args          []string
		code          int
	}{
		{"missing value", "flag needs an argument", []string{"-since"}, 2},
		{"noninteger width", "invalid value", []string{"-width", "wide"}, 2},
		{"negative width", "at least 20", []string{"-width", "-80"}, 1},
		{"invalid end", "invalid 'until' date", []string{"-until", "2026-02-30"}, 1},
		{"future start", "must be on or before", []string{"-since", "2099-01-01"}, 1},
		{"mixed wildcard", "cannot be combined", []string{"-email", "tester@test.com", "-email", "*"}, 1},
		{"blank author", "invalid email address", []string{"-email", " "}, 1},
		{"multiple addresses in one flag", "invalid email address", []string{"-email", "a@test.com, b@test.com"}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"-path", t.TempDir()}, tt.args...)
			code, out, errOut := runForTest(args...)
			if code != tt.code || out != "" || !strings.Contains(errOut, tt.message) {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, out, errOut)
			}
		})
	}
}

func TestRunUnbornRepositoryIsReadable(t *testing.T) {
	root := t.TempDir()
	createTestRepo(t, root, "tester@test.com", 0, testClock)
	code, out, errOut := runForTest("-path", root, "-email", "*")
	if code != 0 || errOut != "" || !strings.Contains(out, "Repositories: 1/1 processed") || !strings.Contains(out, "No commits matched") || !strings.Contains(out, "Total commits: 0 between") {
		t.Fatalf("empty repository appeared unreadable: code=%d\n%s\n%s", code, out, errOut)
	}
}

// Color detection happens when the package initializes, so exercise the real
// process environment rather than changing a color-library global in the test.
func TestCLIReportOutputHelper(t *testing.T) {
	if os.Getenv("GITCS_TEST_REPORT_OUTPUT") != "1" {
		return
	}
	os.Exit(run(context.Background(), []string{
		"-path", os.Getenv("GITCS_TEST_REPORT_ROOT"), "-email", "*",
		"-since", "2026-10-01", "-until", "2026-10-16", "-timezone", "UTC", "-width", "20",
	}, os.Stdout, os.Stderr, testClock))
}

func TestCLIReportNoColorAndNarrowOutput(t *testing.T) {
	root := t.TempDir()
	createTestRepo(t, root, "tester@test.com", 12, testClock)
	for _, noColor := range []string{"", "1"} {
		t.Run("NO_COLOR="+noColor, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestCLIReportOutputHelper$")
			command.Env = append(os.Environ(), "GITCS_TEST_REPORT_OUTPUT=1", "GITCS_TEST_REPORT_ROOT="+root,
				"NO_COLOR="+noColor, "FORCE_COLOR=1", "TERM=xterm-256color")
			var stderr bytes.Buffer
			command.Stderr = &stderr
			output, err := command.Output()
			if err != nil || stderr.Len() != 0 {
				t.Fatalf("report subprocess: %v stderr=%s", err, &stderr)
			}
			if gotColor := strings.Contains(string(output), "\x1b["); gotColor != (noColor == "") {
				t.Fatalf("NO_COLOR=%q: ANSI present=%t", noColor, gotColor)
			}
			lines := strings.Split(plain(string(output)), "\n")
			if !strings.Contains(string(output), "Total commits:") || !strings.Contains(plain(string(output)), "Total commits: 12 between") {
				t.Fatalf("missing report total: %s", output)
			}
			for _, line := range lines {
				// Report metadata is prose; calendar rows and legend must fit the width.
				if (strings.HasPrefix(line, "     ") || strings.HasPrefix(line, "Mon  ") || strings.HasPrefix(line, "Wed  ") || strings.HasPrefix(line, "Fri  ") || strings.HasPrefix(line, "Legend:")) && len(line) > 20 {
					t.Fatalf("calendar exceeded width 20: %q", line)
				}
			}
		})
	}
}
