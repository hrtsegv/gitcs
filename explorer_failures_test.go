package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/go-git/go-git/v5/plumbing"
)

func TestExplorerCancelledSearchRestoresReadingPosition(t *testing.T) {
	m := testExplorer(t)
	m.discovery, m.loading = false, true
	m.repos = []string{"/one", "/two"}
	var records []CommitRecord
	for i := range 20 {
		records = append(records, explorerRecord(i+1, fmt.Sprintf("change %02d\n%s", i, strings.Repeat("message paragraph\n", 40)), "2026-10-16T12:00:00Z"))
	}
	m.builder.add(repositoryResult{path: "/one", commits: records})
	m.refreshSelection(false)
	press(m, tea.KeyEnter)
	press(m, tea.KeyEnd)
	press(m, tea.KeyEnter)
	press(m, tea.KeyEnd)
	hash, date := m.selectedCommit().Hash, m.date
	listOffset, detailOffset := m.listOffset, m.details.YOffset()
	if listOffset == 0 || detailOffset == 0 {
		t.Fatal("fixture did not scroll the commit list and details")
	}
	press(m, '/')
	m.Update(tea.PasteMsg{Content: "change 00"})
	if m.selectedCommit().Hash == hash {
		t.Fatal("fixture did not change the search selection")
	}
	// The original commit can move to a different list index while searching.
	m.Update(repositoryMsg{generation: m.generation, result: repositoryResult{path: "/two", commits: []CommitRecord{
		explorerRecord(99, "newly arrived", "2026-10-16T16:00:00Z"),
	}}})
	press(m, tea.KeyEscape)
	if m.search.Value() != "" || m.searching || m.focus != focusDetails || !m.date.Equal(date) || m.selectedCommit().Hash != hash {
		t.Fatalf("cancelled search lost the original reading context: focus=%d, selected=%s, date=%s, search=%q", m.focus, m.selectedCommit().Hash, m.date, m.search.Value())
	}
	if m.listOffset < listOffset || m.details.YOffset() != detailOffset || !strings.Contains(m.details.View(), "message paragraph") {
		t.Fatalf("cancelled search lost scroll positions: list=%d (was %d), details=%d (was %d)", m.listOffset, listOffset, m.details.YOffset(), detailOffset)
	}
}

func TestExplorerRefreshRecoversFromDiscoveryAndHistoryFailures(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	m := testExplorer(t)
	m.root = root
	drain := func(cmd tea.Cmd) {
		t.Helper()
		for cmd != nil {
			_, cmd = m.Update(cmd())
		}
	}
	drain(m.Init())
	if m.loading || m.scanErr == nil || !strings.Contains(m.statusLine(), "Failed") {
		t.Fatal("a missing root should finish in an actionable failed state")
	}
	press(m, 'w')
	if !strings.Contains(m.overlayView.View(), "workspace") {
		t.Fatal("the failed root was not identified in warnings")
	}
	press(m, tea.KeyEscape)
	good := filepath.Join(root, "good")
	createTestRepo(t, good, "tester@test.com", 1, testClock)
	bad := filepath.Join(root, "broken")
	if err := os.MkdirAll(filepath.Join(bad, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	drain(press(m, 'R'))
	if m.scanErr != nil || m.loading || m.completed != 2 || len(m.visible) != 1 || len(m.builder.report.Diagnostics) != 1 || !strings.Contains(m.statusLine(), "Partial results") {
		t.Fatalf("one broken repository hid healthy results or retained the discovery error: %s, error=%v, diagnostics=%v", m.statusLine(), m.scanErr, m.builder.report.Diagnostics)
	}
	press(m, 'w')
	if !strings.Contains(m.overlayView.View(), "broken") {
		t.Fatalf("warnings did not identify the unreadable repository: %s", m.overlayView.View())
	}
	press(m, tea.KeyEscape)
	if err := os.RemoveAll(bad); err != nil {
		t.Fatal(err)
	}
	drain(press(m, 'R'))
	if m.loading || m.scanErr != nil || len(m.builder.report.Diagnostics) != 0 || len(m.visible) != 1 || !strings.Contains(m.statusLine(), "Ready") {
		t.Fatalf("a successful refresh did not clear old failures: %s", m.statusLine())
	}
}

func TestExplorerRefreshDropsMissingRepositoryFilterAndKeepsSearch(t *testing.T) {
	root := t.TempDir()
	removed := filepath.Join(root, "removed")
	remaining := filepath.Join(root, "remaining")
	createTestRepo(t, removed, "tester@test.com", 1, testClock)
	createTestRepo(t, remaining, "tester@test.com", 2, testClock)
	m := testExplorer(t)
	m.root = root
	drain := func(cmd tea.Cmd) {
		t.Helper()
		for cmd != nil {
			_, cmd = m.Update(cmd())
		}
	}
	drain(m.Init())
	m.repo = resolvedPath(t, removed)
	m.search.SetValue("commit")
	m.refreshSelection(false)
	if len(m.visible) != 1 {
		t.Fatalf("fixture did not select one repository: %d", len(m.visible))
	}
	if err := os.RemoveAll(removed); err != nil {
		t.Fatal(err)
	}
	drain(press(m, 'R'))
	if m.repo != "" || m.search.Value() != "commit" || len(m.visible) != 2 || m.matchingCount() != 2 || len(m.builder.report.Commits) != 2 {
		t.Fatalf("missing repository left a stale filter or changed the search: repo=%q, search=%q, visible=%d, matching=%d", m.repo, m.search.Value(), len(m.visible), m.matchingCount())
	}
}

func TestExplorerPatchSurvivesTemporaryTinyTerminalWithWidthCap(t *testing.T) {
	m := testExplorer(t)
	loadExplorer(m)
	m.widthCap = 40
	hash := m.selectedCommit().Hash
	m.loadPatch = func(context.Context, plumbing.Hash, []string) (CommitPatch, error) {
		return CommitPatch{Repository: "/one", Text: "+界🙂 patch after resize"}, nil
	}
	cmd := press(m, 'p')
	m.Update(tea.WindowSizeMsg{Width: 1, Height: 1})
	if view := m.View().Content; ansi.StringWidth(view) > 1 || strings.Contains(view, "\n") {
		t.Fatalf("tiny terminal overflowed: %q", view)
	}
	m.Update(cmd())
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	if !m.patch.ready || m.overlay != "patch" || m.selectedCommit().Hash != hash || !strings.Contains(m.View().Content, "+界🙂 patch after resize") {
		t.Fatal("temporary tiny terminal lost a pending patch or the selected commit")
	}
	for _, line := range strings.Split(m.View().Content, "\n") {
		if ansi.StringWidth(line) > 40 {
			t.Fatalf("resizing ignored the command-line width cap: %q", line)
		}
	}
	press(m, tea.KeyEscape)
	if m.overlay != "" || m.selectedCommit().Hash != hash {
		t.Fatal("return from the resized patch lost the selected commit")
	}
}
