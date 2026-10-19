package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/go-git/go-git/v5/plumbing"
)

func TestExplorerPatchIsLazyAndReturnsToSelection(t *testing.T) {
	m := testExplorer(t)
	m.discovery, m.loading = false, false
	var records []CommitRecord
	for i := range 25 {
		record := explorerRecord(i+1, fmt.Sprintf("change %02d\n%s", i, strings.Repeat("Details paragraph\n", 60)), "2026-10-16T12:00:00Z")
		records = append(records, record)
	}
	m.builder.add(repositoryResult{path: "/one", commits: records})
	m.refreshSelection(false)
	calls := 0
	m.loadPatch = func(context.Context, plumbing.Hash, []string) (CommitPatch, error) {
		calls++
		return CommitPatch{Repository: "/one", Text: strings.Repeat("+patch line\n", 60)}, nil
	}
	press(m, tea.KeyUp)
	press(m, tea.KeyDown)
	press(m, tea.KeyEnter)
	press(m, tea.KeyEnd)
	press(m, tea.KeyEnter)
	press(m, tea.KeyEnd)
	if calls != 0 || m.listOffset == 0 || m.details.YOffset() == 0 {
		t.Fatal("navigation should scroll the list/details without loading a patch")
	}
	date, hash := m.date, m.selectedCommit().Hash
	listOffset, detailOffset := m.listOffset, m.details.YOffset()
	cmd := press(m, 'p')
	if cmd == nil || !m.patch.loading || m.overlay != "patch" || calls != 0 {
		t.Fatal("p should display loading immediately and schedule the patch lazily")
	}
	m.Update(cmd())
	press(m, tea.KeyDown)
	patchOffset := m.patch.view.YOffset()
	press(m, tea.KeyEscape)
	if m.overlay != "" || m.focus != focusDetails || !m.date.Equal(date) || m.selectedCommit().Hash != hash || m.listOffset != listOffset || m.details.YOffset() != detailOffset {
		t.Fatal("returning from the patch lost the date, commit, list, focus, or detail scroll position")
	}
	if cmd := press(m, 'p'); cmd != nil || calls != 1 || m.patch.view.YOffset() != patchOffset {
		t.Fatal("reopening the current successful patch should preserve it without loading again")
	}
	press(m, 'p')
	press(m, tea.KeyEscape)
	press(m, tea.KeyHome)
	if m.selectedCommit().Hash == hash || m.patch.ready {
		t.Fatal("changing the selected commit should discard the previous patch")
	}
	cmd = press(m, 'p')
	if cmd == nil {
		t.Fatal("a newly selected commit did not request its own patch")
	}
	m.Update(cmd())
	press(m, tea.KeyEscape)
	press(m, 'R')
	if m.patch.ready || m.patch.hash != plumbing.ZeroHash {
		t.Fatal("refresh retained a patch from the previous scan")
	}
	if cmd := press(m, 'p'); cmd != nil || m.overlay == "patch" {
		t.Fatal("an empty selection should not open or load a patch")
	}
}

func TestExplorerPatchCancellation(t *testing.T) {
	for _, action := range []string{"close", "refresh", "quit", "interrupt"} {
		t.Run(action, func(t *testing.T) {
			m := testExplorer(t)
			loadExplorer(m)
			started := make(chan context.Context, 1)
			finished := make(chan tea.Msg, 1)
			m.loadPatch = func(ctx context.Context, _ plumbing.Hash, _ []string) (CommitPatch, error) {
				started <- ctx
				<-ctx.Done()
				return CommitPatch{}, ctx.Err()
			}
			cmd := press(m, 'p')
			go func() { finished <- cmd() }()
			var patchCtx context.Context
			select {
			case patchCtx = <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("patch loader did not start")
			}
			switch action {
			case "close":
				press(m, tea.KeyEscape)
			case "refresh":
				m.refresh()
			case "quit":
				press(m, 'q')
			case "interrupt":
				m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			}
			if patchCtx.Err() != context.Canceled {
				t.Fatal("leaving the patch did not cancel its loader")
			}
			select {
			case msg := <-finished:
				if action == "close" || action == "refresh" {
					m.Update(msg)
					if m.overlay == "patch" || m.patch.ready || m.patch.err != nil {
						t.Fatal("a cancelled result replaced the screen after leaving the patch")
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled patch loader did not return")
			}
		})
	}
}

func TestExplorerPatchRetryIgnoresStaleResults(t *testing.T) {
	m := testExplorer(t)
	loadExplorer(m)
	m.loadPatch = func(context.Context, plumbing.Hash, []string) (CommitPatch, error) {
		return CommitPatch{Repository: "/one", Text: "+fresh patch"}, nil
	}
	first := press(m, 'p')()
	cmd := press(m, 'R')
	current := cmd().(patchMsg)
	wrongGeneration, wrongRequest, wrongHash := current, current, current
	wrongGeneration.generation--
	wrongRequest.request--
	wrongHash.hash = plumbing.NewHash(strings.Repeat("f", 40))
	for _, stale := range []tea.Msg{first, wrongGeneration, wrongRequest, wrongHash} {
		m.Update(stale)
		if !m.patch.loading || m.patch.ready || m.patch.data.Text != "" {
			t.Fatal("a stale generation, request, or commit replaced the pending patch")
		}
	}
	m.Update(current)
	if !m.patch.ready || !strings.Contains(m.patch.view.View(), "+fresh patch") {
		t.Fatal("the current retry result was not displayed")
	}
	current.err = errors.New("late error")
	m.Update(current)
	if m.patch.err != nil || !m.patch.ready {
		t.Fatal("a duplicate late completion replaced the successful patch")
	}
	press(m, tea.KeyEscape)
	// Closing and reopening a pending request for the same hash is another
	// source of late results, independently of pressing R to retry.
	retry := press(m, 'p')
	if retry != nil {
		t.Fatal("successful current patch was not cached")
	}
	old := press(m, 'R')
	press(m, tea.KeyEscape)
	newer := press(m, 'p')
	m.Update(old())
	if !m.patch.loading || m.patch.ready {
		t.Fatal("completion from a closed request replaced the reopened patch")
	}
	m.Update(newer())
	if !m.patch.ready {
		t.Fatal("reopened patch did not accept its current result")
	}
}

func TestExplorerPatchRepositorySnapshotAndPreference(t *testing.T) {
	m := testExplorer(t)
	loadExplorer(m)
	m.selectedCommit().Repositories = []string{"/z", "/two", "/a"}
	m.repo = "/two"
	m.refreshSelection(false)
	before := slices.Clone(m.selectedCommit().Repositories)
	var received []string
	m.loadPatch = func(_ context.Context, _ plumbing.Hash, paths []string) (CommitPatch, error) {
		received = slices.Clone(paths)
		return CommitPatch{Repository: paths[0], Text: "+change"}, nil
	}
	cmd := press(m, 'p')
	if !slices.Equal(m.selectedCommit().Repositories, before) {
		t.Fatal("ordering patch fallback repositories mutated the shared commit")
	}
	commit := *m.selectedCommit()
	m.Update(repositoryMsg{generation: m.generation, result: repositoryResult{path: "/arrived", commits: []CommitRecord{commit}}})
	m.Update(cmd())
	if !slices.Equal(received, []string{"/two", "/a", "/z"}) {
		t.Fatalf("patch command should keep the selected repository first and its original snapshot: %v", received)
	}
	if !slices.Contains(m.selectedCommit().Repositories, "/arrived") || !slices.Equal(m.selectedCommit().Repositories[:len(before)], before) {
		t.Fatal("patch fallback ordering corrupted memberships received during loading")
	}
	press(m, tea.KeyEscape)
	press(m, 'r')
	press(m, tea.KeyHome)
	press(m, tea.KeyEnter)
	if cmd := press(m, 'p'); cmd == nil {
		t.Fatal("changing the preferred repository should reload the cached patch")
	}
}

func TestExplorerPatchHelpKeepsPendingCompletion(t *testing.T) {
	m := testExplorer(t)
	m.builder.add(repositoryResult{path: "/one", commits: []CommitRecord{
		explorerRecord(1, "earlier result", "2026-10-15T12:00:00Z"),
	}})
	m.refreshSelection(false)
	hash := m.selectedCommit().Hash
	m.loadPatch = func(context.Context, plumbing.Hash, []string) (CommitPatch, error) {
		return CommitPatch{Repository: "/one", Text: "+completed while help was open"}, nil
	}
	cmd := press(m, 'p')
	press(m, '?')
	if m.overlay != "help" || m.overlayBack != "patch" {
		t.Fatal("patch help did not remember the patch screen")
	}
	m.Update(tea.WindowSizeMsg{Width: 32, Height: 20})
	m.Update(repositoryMsg{generation: m.generation, result: repositoryResult{path: "/two", commits: []CommitRecord{
		explorerRecord(2, "later result", "2026-10-16T12:00:00Z"),
	}}})
	m.Update(cmd())
	if m.overlay != "help" || !m.patch.ready || m.selectedCommit().Hash != hash || m.date.Format(dateLayout) != "2026-10-15" {
		t.Fatal("late scan/patch results should retain help, the selected date, and the patch result")
	}
	press(m, tea.KeyEscape)
	if m.overlay != "patch" || !strings.Contains(m.View().Content, "+completed while help was open") {
		t.Fatal("Escape from help did not return to the completed patch")
	}
}

func TestExplorerPatchStatesFitWithoutTerminalControls(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	unsafe := "\x1b[31m界🙂\x1b[0m\x1b]52;c;clipboard-secret\a\x00\r\u202e"
	for _, state := range []struct {
		name string
		data CommitPatch
		err  error
		want string
	}{
		{name: "loading", want: "Loading patch"},
		{name: "changes", data: CommitPatch{Repository: "/one", Text: "+\t" + unsafe + strings.Repeat("wide 界🙂", 100)}, want: "Changes in this commit"},
		{name: "empty", want: "This commit has no file"},
		{name: "error", err: errors.New(unsafe + strings.Repeat("Cannot read history ", 100)), want: "Could not load this patch"},
		{name: "merge", data: CommitPatch{Repository: "/one", Text: "+merge", Merge: true}, want: "Merge: compared with first"},
		{name: "truncated merge", data: CommitPatch{Repository: "/one", Text: "+partial", Truncated: true, Merge: true}, want: "Truncated at 1 MiB | first"},
	} {
		t.Run(state.name, func(t *testing.T) {
			m := newExplorer(context.Background(), "/workspace", testQuery(t, "2026-09-01", "2026-10-16", "UTC"), 0)
			t.Cleanup(m.cancel)
			loadExplorer(m)
			m.selectedCommit().Message = "subject " + unsafe
			m.loadPatch = func(context.Context, plumbing.Hash, []string) (CommitPatch, error) { return state.data, state.err }
			cmd := press(m, 'p')
			if state.name != "loading" {
				m.Update(cmd())
			}
			for _, size := range [][2]int{{32, 20}, {80, 24}, {120, 40}} {
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				content := m.View().Content
				assertPatchViewFits(t, content, size[0], size[1])
				if m.color || strings.ContainsAny(content, "\x1b\x00\r\u202e") || strings.Contains(content, "clipboard-secret") {
					t.Fatalf("NO_COLOR patch retained terminal control content: %q", content)
				}
				if !strings.Contains(content, state.want) {
					t.Fatalf("missing %q in %s at %v: %s", state.want, state.name, size, content)
				}
			}
			if state.err != nil {
				if cmd := press(m, 'R'); cmd == nil || !m.patch.loading {
					t.Fatal("failed patch did not offer a working retry")
				}
			}
		})
	}
}

func TestExplorerPatchScrollPanAndResize(t *testing.T) {
	m := testExplorer(t)
	loadExplorer(m)
	var lines []string
	for i := range 100 {
		lines = append(lines, fmt.Sprintf("+%03d %s line end", i, strings.Repeat("界🙂", 50)))
	}
	m.loadPatch = func(context.Context, plumbing.Hash, []string) (CommitPatch, error) {
		return CommitPatch{Repository: "/one", Text: strings.Join(lines, "\n")}, nil
	}
	m.Update(press(m, 'p')())
	before := m.View().Content
	press(m, tea.KeyRight)
	if m.patch.view.XOffset() == 0 || before == m.View().Content {
		t.Fatal("right arrow did not pan a long Unicode patch line")
	}
	press(m, tea.KeyPgDown)
	if m.patch.view.YOffset() == 0 {
		t.Fatal("PageDown did not scroll the patch")
	}
	press(m, tea.KeyHome)
	if !m.patch.view.AtTop() {
		t.Fatal("Home did not return to the start of the patch")
	}
	m.Update(tea.WindowSizeMsg{Width: 32, Height: 20})
	press(m, tea.KeyEnd)
	for range 100 {
		press(m, tea.KeyRight)
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.patch.view.PastBottom() || m.patch.view.XOffset() > ansi.StringWidth(lines[0])-120 {
		t.Fatal("growing the terminal left the patch past its scroll bounds")
	}
	content := m.View().Content
	if !strings.Contains(content, "line end") || !strings.Contains(content, "Esc/p back") {
		t.Fatal("horizontal panning split patch lines or hid the footer after resize")
	}
	assertPatchViewFits(t, content, 120, 40)
	press(m, tea.KeyPgUp)
	press(m, tea.KeyLeft)
	assertPatchViewFits(t, m.View().Content, 120, 40)
}

func TestExplorerPatchColoredContentIsSanitized(t *testing.T) {
	m := testExplorer(t)
	loadExplorer(m)
	m.color = true
	m.selectedCommit().Message = "safe subject\x1b]52;c;subject-secret\a"
	m.loadPatch = func(context.Context, plumbing.Hash, []string) (CommitPatch, error) {
		return CommitPatch{Repository: "/one", Text: "+\t界🙂 added\x1b]52;c;patch-secret\a\x00\u202e\n-removed\n@@ hunk @@"}, nil
	}
	m.Update(press(m, 'p')())
	content := m.View().Content
	if strings.Contains(content, "\x1b]") || strings.Contains(content, "secret") || strings.ContainsAny(content, "\x00\u202e") {
		t.Fatal("untrusted terminal controls survived colored patch rendering")
	}
	if !strings.Contains(ansi.Strip(content), "+    界🙂 added") {
		t.Fatal("patch rendering lost Unicode text or tab indentation")
	}
	assertPatchViewFits(t, content, 80, 24)
}

func TestClipPatchLinePreservesDisplayColumns(t *testing.T) {
	for _, tt := range []struct {
		name        string
		line        string
		left, width int
		want        string
	}{
		{"ASCII", "abcdef", 2, 3, "cde"},
		{"CJK at left", "A界B", 2, 2, " B"},
		{"CJK at right", "A界B", 0, 2, "A "},
		{"complete CJK", "A界B", 1, 2, "界"},
		{"emoji at left", "A🙂B", 2, 2, " B"},
		{"emoji at right", "A🙂B", 0, 2, "A "},
		{"complete emoji", "A🙂B", 1, 2, "🙂"},
		{"combining character", "Ae\u0301🙂B", 1, 3, "e\u0301🙂"},
		{"combining character at right", "e\u0301Z", 0, 1, "e\u0301"},
		{"combining character excluded with base", "e\u0301Z", 1, 1, "Z"},
		{"ZWJ cluster", "A👩‍💻B", 1, 2, "👩‍💻"},
		{"ZWJ cluster at left", "A👩‍💻B", 2, 2, " B"},
		{"ZWJ cluster at right", "A👩‍💻B", 0, 2, "A "},
		{"empty", "", 0, 10, ""},
		{"past the end", "abc", 10, 5, ""},
		{"zero width", "abc", 0, 0, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, colored := range []bool{false, true} {
				line := tt.line
				if colored {
					line = "\x1b[32m" + line + "\x1b[0m"
				}
				got := clipPatchLine(line, tt.left, tt.width)
				if plain := ansi.Strip(got); plain != tt.want || !utf8.ValidString(plain) || ansi.StringWidth(plain) > tt.width {
					t.Fatalf("colored=%t: clip(%q, %d, %d) = %q, want %q", colored, tt.line, tt.left, tt.width, plain, tt.want)
				}
				if !colored && strings.Contains(got, "\x1b") {
					t.Fatal("plain patch clipping added terminal sequences")
				}
				if colored && tt.want != "" && (!strings.Contains(got, "\x1b[32m") || !strings.Contains(got, "\x1b[0m")) {
					t.Fatal("patch clipping lost its generated color or closing reset")
				}
			}
		})
	}
	// A wide character crossing the left edge must not shift the final column
	// out of sight when the user pans all the way to the right.
	line := "+000 " + strings.Repeat("界🙂", 50) + " line end"
	got := clipPatchLine(line, ansi.StringWidth(line)-120, 120)
	if ansi.StringWidth(got) != 120 || !strings.HasSuffix(got, "line end") {
		t.Fatalf("the final column was lost while clipping wide text: %q", got)
	}
}

func assertPatchViewFits(t *testing.T, content string, width, height int) {
	t.Helper()
	if !utf8.ValidString(content) {
		t.Fatal("patch viewport split a Unicode character")
	}
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		t.Fatalf("patch exceeds %d rows: %d", height, len(lines))
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > width {
			t.Fatalf("patch exceeds %d columns: %q", width, line)
		}
	}
}
