package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/go-git/go-git/v5/plumbing"
)

func testExplorer(t *testing.T) *explorer {
	t.Helper()
	m := newExplorer(context.Background(), "/workspace", testQuery(t, "2026-09-01", "2026-10-16", "UTC"), 0)
	m.now = func() time.Time { return testClock }
	m.color = false
	t.Cleanup(m.cancel)
	return m
}

func press(m *explorer, code rune) tea.Cmd {
	key := tea.KeyPressMsg{Code: code}
	if code >= 32 && code < 127 {
		key.Text = string(code)
	}
	_, cmd := m.Update(key)
	return cmd
}

func explorerRecord(n int, subject, when string) CommitRecord {
	author, _ := time.Parse(time.RFC3339, when)
	return CommitRecord{
		Hash: plumbing.NewHash(fmt.Sprintf("%040x", n)), Message: subject,
		AuthorName: "Tester", AuthorEmail: "tester@test.com",
		AuthorWhen: author, CommitterWhen: author, Repositories: []string{"/one"},
	}
}

func loadExplorer(m *explorer) {
	m.discovery, m.loading = false, false
	m.repos = []string{"/one", "/two"}
	m.builder.add(repositoryResult{path: "/one", commits: []CommitRecord{
		explorerRecord(1, "older change", "2026-10-15T12:00:00Z"),
		explorerRecord(2, "newest change\n\nFull message paragraph", "2026-10-16T12:00:00Z"),
		explorerRecord(3, "second same day", "2026-10-16T10:00:00Z"),
	}})
	m.builder.add(repositoryResult{path: "/two", commits: []CommitRecord{
		explorerRecord(2, "newest change\n\nFull message paragraph", "2026-10-16T12:00:00Z"),
	}})
	m.refreshSelection(false)
}

func TestExplorerCalendarAndPaneNavigation(t *testing.T) {
	m := testExplorer(t)
	loadExplorer(m)
	if len(m.visible) != 2 || m.days["2026-10-16"] != 2 {
		t.Fatalf("initial day / dedup: %v %v", m.visible, m.days)
	}
	press(m, tea.KeyUp)
	if m.date.Format(dateLayout) != "2026-10-15" || len(m.visible) != 1 || m.autoDate {
		t.Fatalf("day movement: %s %v", m.date, m.visible)
	}
	press(m, tea.KeyLeft)
	if m.date.Format(dateLayout) != "2026-10-08" {
		t.Fatal("left should move one week")
	}
	press(m, tea.KeyHome)
	press(m, tea.KeyUp)
	if !m.date.Equal(m.query.Boundary.Since) {
		t.Fatal("calendar moved before boundary")
	}
	press(m, tea.KeyEnd)
	press(m, tea.KeyDown)
	if m.date.Format(dateLayout) != "2026-10-16" {
		t.Fatal("calendar moved after boundary")
	}
	press(m, tea.KeyEnter)
	press(m, tea.KeyDown)
	hash := m.selectedCommit().Hash
	press(m, tea.KeyEnter)
	if m.focus != focusDetails || !strings.Contains(m.details.View(), hash.String()) {
		t.Fatal("Enter did not open selected commit details")
	}
	m.Update(tea.WindowSizeMsg{Width: 42, Height: 22})
	press(m, tea.KeyEscape)
	if m.focus != focusCommits || m.selectedCommit().Hash != hash {
		t.Fatal("resize/back lost selection")
	}
}

func TestExplorerFiltersKeepUniqueCounts(t *testing.T) {
	m := testExplorer(t)
	loadExplorer(m)
	press(m, 'r')
	press(m, tea.KeyEnd)
	press(m, tea.KeyEnter)
	if m.repo != "/two" || len(m.visible) != 1 || m.days["2026-10-16"] != 1 {
		t.Fatalf("repository filter: %q %v %v", m.repo, m.visible, m.days)
	}
	m.details.GotoBottom()
	if !strings.Contains(m.details.View(), "/one") || !strings.Contains(m.details.View(), "/two") {
		t.Fatal("shared commit lost its repository memberships")
	}
	press(m, 'r')
	press(m, tea.KeyHome)
	press(m, tea.KeyEnter)
	press(m, 'd')
	if len(m.visible) != 3 || m.days["2026-10-16"] != 2 {
		t.Fatal("all dates / all repos should count a shared hash once")
	}
	press(m, '/')
	for _, r := range "older" {
		press(m, r)
	}
	press(m, tea.KeyEnter)
	if len(m.visible) != 1 || m.selectedCommit().Hash != plumbing.NewHash(fmt.Sprintf("%040x", 1)) || len(m.days) != 1 {
		t.Fatal("search should cover all loaded dates and update counts")
	}
	press(m, '/')
	if cmd := press(m, 'q'); cmd != nil && !m.searching {
		t.Fatal("q in search quit the application")
	}
	if m.ctx.Err() != nil || m.search.Value() != "olderq" {
		t.Fatal("search did not own printable shortcuts")
	}
	press(m, tea.KeyEscape)
	if m.search.Value() != "older" || len(m.visible) != 1 {
		t.Fatal("cancelled search did not restore previous value")
	}
	press(m, tea.KeyEscape)
	if m.search.Value() != "" || len(m.visible) != 3 {
		t.Fatal("calendar Escape did not clear accepted search")
	}
	press(m, '/')
	m.Update(tea.PasteMsg{Content: "older"})
	if m.search.Value() != "older" || len(m.visible) != 1 {
		t.Fatal("pasted search did not update filters")
	}
}

func TestExplorerStreamsRealRepositoriesAndWarnings(t *testing.T) {
	root := t.TempDir()
	createTestRepo(t, filepath.Join(root, "good"), "tester@test.com", 2, testClock)
	m := testExplorer(t)
	m.root = root
	msg := m.Init()()
	_, next := m.Update(msg)
	if next == nil || m.discovery || !m.loading || len(m.repos) != 1 {
		t.Fatal("discovery did not start history loading")
	}
	for next != nil {
		msg = next()
		_, next = m.Update(msg)
	}
	if m.loading || m.completed != 1 || len(m.visible) != 2 {
		t.Fatalf("stream did not finish: %s", m.statusLine())
	}
	m.builder.add(repositoryResult{path: `broken\repo`, err: errors.New("unreadable")})
	m.refreshSelection(true)
	press(m, 'w')
	if !strings.Contains(m.overlayView.View(), `"broken\\repo"`) || !strings.Contains(m.overlayView.View(), "unreadable") {
		t.Fatalf("warnings unavailable: %s", m.overlayView.View())
	}
	if !strings.Contains(m.statusLine(), "Partial results") {
		t.Fatal("partial results not visible")
	}
}

func TestExplorerRefreshIgnoresStaleEventsAndCancels(t *testing.T) {
	m := testExplorer(t)
	loadExplorer(m)
	oldCtx, oldGeneration := m.ctx, m.generation
	press(m, 'R')
	if oldCtx.Err() != context.Canceled || m.ctx.Err() != nil || len(m.visible) != 0 {
		t.Fatal("refresh did not cancel and clear old scan")
	}
	m.Update(discoveredMsg{generation: oldGeneration, scan: ScanResult{Repos: []string{"stale"}}})
	m.Update(repositoryMsg{generation: oldGeneration, result: repositoryResult{path: "stale", commits: []CommitRecord{explorerRecord(9, "stale", "2026-10-16T12:00:00Z")}}})
	if len(m.repos) != 0 || len(m.builder.report.Commits) != 0 {
		t.Fatal("stale scan mutated refreshed explorer")
	}
	press(m, 'q')
	if m.ctx.Err() != context.Canceled || m.exitErr != nil {
		t.Fatal("normal quit should cancel work and succeed")
	}
	other := testExplorer(t)
	other.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if other.exitErr != context.Canceled || other.ctx.Err() != context.Canceled {
		t.Fatal("Ctrl+C should cancel with interrupt exit status")
	}
}

func TestExplorerRefreshIncludesNewCommits(t *testing.T) {
	root := t.TempDir()
	repo := createTestRepo(t, root, "tester@test.com", 1, testClock)
	m := testExplorer(t)
	m.root = root
	load := func(cmd tea.Cmd) {
		for cmd != nil {
			_, cmd = m.Update(cmd())
		}
	}
	load(m.Init())
	if len(m.visible) != 1 {
		t.Fatal("initial scan did not load the fixture")
	}
	later := testClock.Add(time.Minute)
	addTestCommit(t, repo, "created after launch", "tester@test.com", later, later)
	m.now = func() time.Time { return later.Add(time.Minute) }
	load(press(m, 'R'))
	if len(m.visible) != 2 || m.selectedCommit().Message != "created after launch" {
		t.Fatal("refresh excluded a commit created after the explorer opened")
	}
}

func TestExplorerLoadingKeepsSelection(t *testing.T) {
	m := testExplorer(t)
	loadExplorer(m)
	press(m, tea.KeyEnter)
	press(m, tea.KeyDown)
	selected := m.selectedCommit().Hash
	m.Update(repositoryMsg{generation: m.generation, result: repositoryResult{path: "/three", commits: []CommitRecord{
		explorerRecord(4, "arriving newest", "2026-10-16T16:00:00Z"),
	}}})
	m.Update(repositoryMsg{generation: m.generation, done: true})
	if m.selectedCommit().Hash != selected {
		t.Fatal("incremental results moved the selected commit")
	}
	// Explicitly navigating an empty day prevents late results moving the cursor.
	m.focus = focusCalendar
	press(m, tea.KeyHome)
	date := m.date
	m.Update(repositoryMsg{generation: m.generation, result: repositoryResult{path: "/four"}})
	if !m.date.Equal(date) {
		t.Fatal("late result moved an explicitly selected date")
	}
}

func TestExplorerOpeningCommitsFreezesDateDuringLoading(t *testing.T) {
	m := testExplorer(t)
	m.builder.add(repositoryResult{path: "/one", commits: []CommitRecord{
		explorerRecord(1, "earlier result", "2026-10-15T12:00:00Z"),
	}})
	m.refreshSelection(false)
	press(m, tea.KeyEnter)
	press(m, tea.KeyEnter)
	m.Update(repositoryMsg{generation: m.generation, result: repositoryResult{path: "/two", commits: []CommitRecord{
		explorerRecord(2, "later result", "2026-10-16T12:00:00Z"),
	}}})
	if m.date.Format(dateLayout) != "2026-10-15" || m.selectedCommit().Message != "earlier result" {
		t.Fatal("a later repository moved the date while reading details")
	}
}

func TestExplorerViewsFitAndSanitize(t *testing.T) {
	m := testExplorer(t)
	loadExplorer(m)
	m.builder.report.Commits[1].Message = "界🙂 fix\x1b[31m red\x1b[0m\n\x1b]52;c;secret\a\n" + strings.Repeat("Long paragraph 界🙂 ", 100)
	m.refreshSelection(false)
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 20}, {20, 8}, {1, 1}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, overlay := range []string{"", "help", "warnings", "repositories"} {
			for _, focus := range []explorerFocus{focusCalendar, focusDetails} {
				m.focus = focus
				m.overlay = overlay
				m.setOverlayContent()
				view := m.View()
				if !view.AltScreen || strings.Contains(view.Content, "\x1b") || strings.Contains(view.Content, "secret") {
					t.Fatalf("unsafe / unexpected view: %q", view.Content)
				}
				lines := strings.Split(view.Content, "\n")
				if len(lines) > size[1] {
					t.Fatalf("view exceeds height %v: %d", size, len(lines))
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > size[0] {
						t.Fatalf("view exceeds width %v: %q", size, line)
					}
				}
			}
		}
	}
	if safeLine("a\r\n\tb\x00\u202ec") != "a bc" {
		t.Fatal("control characters not removed")
	}
	m.color = true
	m.overlay = ""
	m.focus = focusDetails
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if view := m.View().Content; strings.Contains(view, "\x1b]52") || strings.Contains(view, "secret") {
		t.Fatal("terminal escape injection survived colored rendering")
	}
}

func TestExplorerUsesReportingTimezoneAndCivilDates(t *testing.T) {
	m := testExplorer(t)
	m.query = testQuery(t, "2026-10-01", "2026-10-16", "America/Los_Angeles")
	m.builder = newReportBuilder(m.query)
	m.builder.add(repositoryResult{path: "/one", commits: []CommitRecord{
		explorerRecord(1, "UTC midnight", "2026-10-16T01:00:00Z"),
	}})
	m.refreshSelection(false)
	if m.date.Format(dateLayout) != "2026-10-15" || m.days["2026-10-15"] != 1 || !strings.Contains(m.calendarView(80), "Thu 2026-10-15") {
		t.Fatalf("civil date converted twice: %s %v", m.date, m.days)
	}
}

func TestExplorerEmptyAndFailedStates(t *testing.T) {
	for _, tt := range []struct {
		name    string
		repos   []string
		warning bool
		err     error
		message string
	}{
		{"no repos", nil, false, nil, "No Git repositories found"},
		{"no matches", []string{"/one"}, false, nil, "No matches"},
		{"unreadable subtree", nil, true, nil, "No repositories could be read"},
		{"broken repo", []string{"/one"}, true, nil, "No repositories could be read"},
		{"scan failure", nil, false, errors.New("root removed"), "Scan failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := testExplorer(t)
			m.discovery, m.loading = false, false
			m.repos, m.scanErr = tt.repos, tt.err
			if tt.warning {
				m.builder.report.Diagnostics = []Diagnostic{{Path: "/denied", Err: errors.New("denied")}}
			} else if len(tt.repos) > 0 {
				m.builder.report.Processed = 1
			}
			if !strings.Contains(m.View().Content, tt.message) {
				t.Fatalf("missing %q: %s", tt.message, m.View().Content)
			}
			if (tt.warning || tt.err != nil) && !strings.Contains(m.statusLine(), "Failed") {
				t.Fatal("failed scan looked successful")
			}
		})
	}
	// Keep an already-open warnings pane current when discovery fails.
	m := testExplorer(t)
	press(m, 'w')
	m.Update(discoveredMsg{generation: m.generation, err: errors.New("root removed")})
	if !strings.Contains(m.overlayView.View(), "root removed") {
		t.Fatal("discovery error did not update open warnings pane")
	}
}

func TestExploreCLIHelpAndPipes(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWithInput(context.Background(), []string{"explore", "-help"}, strings.NewReader(""), &out, &errOut, testClock)
	if code != 0 || !strings.Contains(errOut.String(), "Usage: gitcs explore") || !strings.Contains(errOut.String(), "Arrows") {
		t.Fatalf("help: code %d: %s", code, errOut.String())
	}
	errOut.Reset()
	code = runWithInput(context.Background(), []string{"explore", "-email", "*"}, strings.NewReader("q"), &out, &errOut, testClock)
	if code != 1 || !strings.Contains(errOut.String(), "requires an interactive terminal") || out.Len() != 0 {
		t.Fatalf("pipe: code %d: %s", code, errOut.String())
	}
	errOut.Reset()
	code = runWithInput(context.Background(), []string{"explore", "-width", "20"}, strings.NewReader(""), &out, &errOut, testClock)
	if code != 1 || !strings.Contains(errOut.String(), "at least 32 columns") {
		t.Fatal("explorer accepted a width cap below its minimum size")
	}
}

func TestRepositoryStreamCancellationWithoutConsumer(t *testing.T) {
	root := t.TempDir()
	createTestRepo(t, root, "tester@test.com", 2, testClock)
	ctx, cancel := context.WithCancel(context.Background())
	results := readRepositories(ctx, []string{root, root, root}, testQuery(t, "2026-10-01", "2026-10-16", "UTC"))
	// Simulate quitting while workers cannot deliver their results.
	cancel()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-results:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("cancelled workers did not close the stream")
		}
	}
}
