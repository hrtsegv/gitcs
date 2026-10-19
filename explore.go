package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/go-git/go-git/v5/plumbing"
	"golang.org/x/term"
)

type explorerFocus int

const (
	focusCalendar explorerFocus = iota
	focusCommits
	focusDetails
)

type discoveredMsg struct {
	generation int
	scan       ScanResult
	err        error
}

type repositoryMsg struct {
	generation int
	result     repositoryResult
	done       bool
}

type explorer struct {
	parent     context.Context
	ctx        context.Context
	cancel     context.CancelFunc
	generation int
	root       string
	query      Query
	now        func() time.Time
	widthCap   int
	width      int
	height     int
	color      bool

	builder   *reportBuilder
	repos     []string
	results   <-chan repositoryResult
	completed int
	discovery bool
	loading   bool
	scanErr   error
	exitErr   error

	focus      explorerFocus
	date       time.Time // Civil date, stored at UTC midnight just like Boundary.
	autoDate   bool
	allDates   bool
	repo       string
	days       map[string]int
	visible    []int // Indices into the builder's unique commits, newest first.
	selected   int
	listOffset int
	details    viewport.Model
	patch      patchViewer
	loadPatch  func(context.Context, plumbing.Hash, []string) (CommitPatch, error)

	search       textinput.Model
	searching    bool
	searchBefore string
	searchFocus  explorerFocus
	overlay      string
	overlayBack  string
	overlayView  viewport.Model
	repoCursor   int
}

func interactiveTerminal(value any) bool {
	file, ok := value.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func runExplorer(ctx context.Context, in io.Reader, out io.Writer, root string, q Query, width int) error {
	m := newExplorer(ctx, root, q, width)
	defer func() { m.cancel() }()
	_, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out)).Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("explorer: %w", err)
	}
	return m.exitErr
}

func newExplorer(parent context.Context, root string, q Query, width int) *explorer {
	ctx, cancel := context.WithCancel(parent)
	search := textinput.New()
	search.Prompt = "Search: "
	search.Placeholder = "message or commit hash (all dates)"
	search.CharLimit = 256
	m := &explorer{
		parent: parent, ctx: ctx, cancel: cancel, generation: 1,
		root: root, query: q, now: time.Now, widthCap: width, width: 80, height: 24,
		color: os.Getenv("NO_COLOR") == "", builder: newReportBuilder(q),
		date: q.Boundary.Until.AddDate(0, 0, -1), autoDate: true,
		discovery: true, loading: true, days: make(map[string]int),
		search: search, details: viewport.New(), overlayView: viewport.New(),
		patch: patchViewer{view: viewport.New()}, loadPatch: loadCommitPatch,
	}
	m.details.SoftWrap = true
	m.overlayView.SoftWrap = true
	m.resize()
	return m
}

func (m *explorer) Init() tea.Cmd {
	return discoverRepositories(m.ctx, m.root, m.generation)
}

func discoverRepositories(ctx context.Context, root string, generation int) tea.Cmd {
	return func() tea.Msg {
		scan, err := scanGitFolders(ctx, root)
		return discoveredMsg{generation, scan, err}
	}
}

func nextRepository(ctx context.Context, results <-chan repositoryResult, generation int) tea.Cmd {
	return func() tea.Msg {
		select {
		case result, ok := <-results:
			return repositoryMsg{generation, result, !ok}
		case <-ctx.Done():
			return repositoryMsg{generation: generation, done: true}
		}
	}
}

func (m *explorer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
	case patchMsg:
		m.receivePatch(msg)
	case discoveredMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		m.discovery, m.scanErr = false, msg.err
		m.repos = msg.scan.Repos
		m.builder.report.Diagnostics = append(m.builder.report.Diagnostics, msg.scan.Diagnostics...)
		if m.overlay == "warnings" {
			m.setOverlayContent()
		}
		if m.repo != "" && !slices.Contains(m.repos, m.repo) {
			m.repo = ""
		}
		if msg.err != nil {
			m.loading = false
			return m, nil
		}
		m.results = readRepositories(m.ctx, m.repos, m.query)
		return m, nextRepository(m.ctx, m.results, m.generation)
	case repositoryMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		if msg.done {
			m.loading = false
			sort.Slice(m.builder.report.Diagnostics, func(i, j int) bool {
				return m.builder.report.Diagnostics[i].Path < m.builder.report.Diagnostics[j].Path
			})
			m.refreshSelection(true)
			return m, nil
		}
		m.completed++
		m.builder.add(msg.result)
		m.refreshSelection(true)
		return m, nextRepository(m.ctx, m.results, m.generation)
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			m.exitErr = context.Canceled
			m.cancel()
			return m, tea.Quit
		}
		// Search owns printable keys, including q and other global shortcuts.
		if m.searching {
			switch key {
			case "enter", "esc":
				if key == "esc" {
					m.search.SetValue(m.searchBefore)
				}
				m.searching = false
				m.search.Blur()
				m.focus = m.searchFocus
				m.refreshSelection(false)
				return m, nil
			default:
				var cmd tea.Cmd
				m.search, cmd = m.search.Update(msg)
				m.refreshSelection(false)
				return m, cmd
			}
		}
		if key == "q" {
			m.cancel()
			return m, tea.Quit
		}
		if m.overlay != "" {
			return m, m.updateOverlay(msg)
		}
		switch key {
		case "?":
			m.openOverlay("help")
		case "w":
			m.openOverlay("warnings")
		case "p":
			return m, m.openPatch(false)
		case "r":
			m.repoCursor = 0
			if i := slices.Index(m.repos, m.repo); i >= 0 {
				m.repoCursor = i + 1
			}
			m.openOverlay("repositories")
		case "R":
			return m, m.refresh()
		case "/":
			m.searching = true
			m.searchBefore, m.searchFocus = m.search.Value(), m.focus
			return m, m.search.Focus()
		case "d":
			m.allDates = !m.allDates
			m.refreshSelection(false)
		case "tab", "shift+tab":
			m.autoDate = false
			if key == "tab" {
				m.focus = (m.focus + 1) % 3
			} else {
				m.focus = (m.focus + 2) % 3
			}
		case "esc":
			if m.focus > focusCalendar {
				m.focus--
			} else if m.search.Value() != "" {
				m.search.SetValue("")
				m.refreshSelection(false)
			}
		case "enter":
			m.autoDate = false
			if m.focus < focusDetails {
				m.focus++
			}
		default:
			switch m.focus {
			case focusCalendar:
				m.navigateCalendar(key)
			case focusCommits:
				m.navigateCommits(key)
			case focusDetails:
				if key == "home" {
					m.details.GotoTop()
					return m, nil
				}
				if key == "end" {
					m.details.GotoBottom()
					return m, nil
				}
				var cmd tea.Cmd
				m.details, cmd = m.details.Update(msg)
				return m, cmd
			}
		}
	default:
		if m.searching {
			before := m.search.Value()
			var cmd tea.Cmd
			m.search, cmd = m.search.Update(msg)
			if m.search.Value() != before {
				m.refreshSelection(false)
			}
			return m, cmd
		}
	}
	return m, nil
}

func (m *explorer) refresh() tea.Cmd {
	m.discardPatch()
	m.cancel()
	m.ctx, m.cancel = context.WithCancel(m.parent)
	m.generation++
	// A refresh must include commits created since the explorer was opened.
	// Keep the requested civil date range, but advance the future-date cutoff.
	m.query.Now = m.now()
	m.builder = newReportBuilder(m.query)
	m.repos, m.results = nil, nil
	m.completed, m.scanErr = 0, nil
	m.discovery, m.loading = true, true
	m.refreshSelection(false)
	return discoverRepositories(m.ctx, m.root, m.generation)
}

func (m *explorer) navigateCalendar(key string) {
	date := m.date
	switch key {
	case "left", "h":
		date = date.AddDate(0, 0, -7)
	case "right", "l":
		date = date.AddDate(0, 0, 7)
	case "up", "k":
		date = date.AddDate(0, 0, -1)
	case "down", "j":
		date = date.AddDate(0, 0, 1)
	case "pgup":
		date = date.AddDate(0, 0, -28)
	case "pgdown":
		date = date.AddDate(0, 0, 28)
	case "home":
		date = m.query.Boundary.Since
	case "end":
		date = m.query.Boundary.Until.AddDate(0, 0, -1)
	default:
		return
	}
	if date.Before(m.query.Boundary.Since) {
		date = m.query.Boundary.Since
	}
	if !date.Before(m.query.Boundary.Until) {
		date = m.query.Boundary.Until.AddDate(0, 0, -1)
	}
	m.date, m.autoDate = date, false
	m.refreshSelection(false)
}

func (m *explorer) navigateCommits(key string) {
	m.autoDate = false
	previous := m.selected
	switch key {
	case "up", "k":
		m.selected--
	case "down", "j":
		m.selected++
	case "pgup":
		m.selected -= m.bodyHeight() - 1
	case "pgdown":
		m.selected += m.bodyHeight() - 1
	case "home":
		m.selected = 0
	case "end":
		m.selected = len(m.visible) - 1
	case "left", "h":
		m.focus = focusCalendar
	case "right", "l":
		m.focus = focusDetails
	}
	m.selected = max(0, min(m.selected, len(m.visible)-1))
	if previous != m.selected {
		m.updateDetails(true)
	}
	m.ensureVisible()
}

func (m *explorer) selectedCommit() *CommitRecord {
	if m.selected < 0 || m.selected >= len(m.visible) {
		return nil
	}
	index := m.visible[m.selected]
	if index < 0 || index >= len(m.builder.report.Commits) {
		return nil
	}
	return &m.builder.report.Commits[index]
}

func (m *explorer) refreshSelection(keep bool) {
	hash := ""
	if commit := m.selectedCommit(); keep && commit != nil {
		hash = commit.Hash.String()
	}
	m.days = make(map[string]int)
	indices := make([]int, 0, len(m.builder.report.Commits))
	search := strings.ToLower(m.search.Value())
	for i, commit := range m.builder.report.Commits {
		if m.repo != "" && !slices.Contains(commit.Repositories, m.repo) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(safeText(commit.Message)), search) &&
			!strings.Contains(commit.Hash.String(), search) {
			continue
		}
		indices = append(indices, i)
		m.days[dateKey(commit.AuthorWhen, m.query.Boundary.Location)]++
	}
	sort.Slice(indices, func(i, j int) bool {
		a, b := m.builder.report.Commits[indices[i]], m.builder.report.Commits[indices[j]]
		if !a.AuthorWhen.Equal(b.AuthorWhen) {
			return a.AuthorWhen.After(b.AuthorWhen)
		}
		return a.Hash.String() < b.Hash.String()
	})
	if m.autoDate && len(indices) > 0 {
		m.date, _ = time.Parse(dateLayout, dateKey(m.builder.report.Commits[indices[0]].AuthorWhen, m.query.Boundary.Location))
	}
	m.visible = nil
	for _, i := range indices {
		if m.allDates || search != "" || dateKey(m.builder.report.Commits[i].AuthorWhen, m.query.Boundary.Location) == m.date.Format(dateLayout) {
			m.visible = append(m.visible, i)
		}
	}
	m.selected = 0
	for i, index := range m.visible {
		if m.builder.report.Commits[index].Hash.String() == hash {
			m.selected = i
			break
		}
	}
	if !keep {
		m.listOffset = 0
	}
	m.ensureVisible()
	newHash := ""
	if commit := m.selectedCommit(); commit != nil {
		newHash = commit.Hash.String()
	}
	m.updateDetails(!keep || newHash != hash)
	if m.overlay == "warnings" {
		m.setOverlayContent()
	}
}

func (m *explorer) updateDetails(reset bool) {
	m.syncPatchSelection()
	commit := m.selectedCommit()
	if commit == nil {
		m.details.SetContent("Select a date with commits, or press d for all dates.")
	} else {
		var paths []string
		for _, path := range commit.Repositories {
			paths = append(paths, fmt.Sprintf("  %q", path))
		}
		sort.Strings(paths)
		m.details.SetContent(fmt.Sprintf("%s\nAuthor: %s <%s>\nAuthored: %s\n\n%s\n\nCommitted: %s\nRepositories:\n%s",
			commit.Hash, safeLine(commit.AuthorName), safeLine(commit.AuthorEmail),
			commit.AuthorWhen.In(m.query.Boundary.Location).Format(time.RFC3339),
			safeText(commit.Message),
			commit.CommitterWhen.In(m.query.Boundary.Location).Format(time.RFC3339),
			strings.Join(paths, "\n")))
	}
	if reset {
		m.details.GotoTop()
	}
}

func (m *explorer) openOverlay(name string) {
	m.overlayBack = ""
	m.overlay = name
	m.setOverlayContent()
	m.overlayView.GotoTop()
}

func (m *explorer) updateOverlay(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if m.overlay == "patch" {
		return m.updatePatch(msg)
	}
	if key == "esc" || key == "?" && m.overlay == "help" || key == "w" && m.overlay == "warnings" {
		m.overlay, m.overlayBack = m.overlayBack, ""
		return nil
	}
	if m.overlay == "repositories" {
		switch key {
		case "up", "k":
			m.repoCursor--
		case "down", "j":
			m.repoCursor++
		case "home":
			m.repoCursor = 0
		case "end":
			m.repoCursor = len(m.repos)
		case "enter":
			m.repo = ""
			if m.repoCursor > 0 && m.repoCursor <= len(m.repos) {
				m.repo = m.repos[m.repoCursor-1]
			}
			m.overlay = ""
			m.refreshSelection(false)
		}
		m.repoCursor = max(0, min(m.repoCursor, len(m.repos)))
		return nil
	}
	if key == "home" {
		m.overlayView.GotoTop()
		return nil
	}
	if key == "end" {
		m.overlayView.GotoBottom()
		return nil
	}
	var cmd tea.Cmd
	m.overlayView, cmd = m.overlayView.Update(msg)
	return cmd
}

func (m *explorer) setOverlayContent() {
	switch m.overlay {
	case "help":
		m.overlayView.SetContent(`Explore commits

Calendar: arrows or h/j/k/l; left/right move a week, up/down a day.
Home/End: first/last date. PgUp/PgDown: four weeks.
Enter: calendar -> commits -> details. Esc: back. Tab: switch pane.
Commits: up/down or j/k select; PgUp/PgDown and Home/End jump.
Details: up/down or j/k scroll; PgUp/PgDown, Home/End.

p  Open the selected commit's patch (loaded on demand)
   Patch: up/down scroll, left/right pan; PgUp/PgDown, Home/End.
   Esc or p returns; R reloads; ? shows help.
   Merge patches compare against the first parent. Output is capped at 1 MiB.

d  Toggle selected day / all dates
/  Search loaded messages and hashes across all dates
   Enter accepts; Esc restores the previous search.
   Esc on the calendar clears an accepted search.
r  Filter by repository (shared commits still counted once)
w  Inspect warnings and skipped repositories
R  Refresh the scan
?  Help
q  Quit     Ctrl+C  Cancel and quit

Counts use author dates in the selected timezone. Only the authors,
dates and branch scope supplied on the command line are loaded.
Results appear as repositories finish. Filters apply to loaded results.
Resizing preserves your selection. NO_COLOR disables colored text.`)
	case "warnings":
		lines := []string{"Warnings / skipped repositories", ""}
		if m.scanErr != nil {
			lines = append(lines, safeText(m.scanErr.Error()), "")
		}
		for _, diagnostic := range m.builder.report.Diagnostics {
			lines = append(lines, fmt.Sprintf("%q\n  %s\n", diagnostic.Path, safeText(diagnostic.Err.Error())))
		}
		if len(lines) == 2 {
			lines = append(lines, "No warnings so far.")
		}
		m.overlayView.SetContent(strings.Join(lines, "\n"))
	}
}
