package main

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/go-git/go-git/v5/plumbing"
)

type patchMsg struct {
	generation int
	request    int
	hash       plumbing.Hash
	data       CommitPatch
	err        error
}

// Cache only the selected patch. A request number also identifies retries and
// cancelled loads for the same hash within a single scan generation.
type patchViewer struct {
	request    int
	cancel     context.CancelFunc
	hash       plumbing.Hash
	subject    string
	preference string
	loading    bool
	ready      bool
	data       CommitPatch
	err        error
	view       viewport.Model
	lines      []string
}

func (m *explorer) openPatch(reload bool) tea.Cmd {
	commit := m.selectedCommit()
	if commit == nil {
		return nil
	}
	m.autoDate = false
	m.overlay, m.overlayBack = "patch", ""
	if !reload && m.patch.ready && m.patch.hash == commit.Hash && m.patch.preference == m.repo {
		return nil
	}
	m.stopPatchLoad()
	m.patch.hash = commit.Hash
	m.patch.subject = strings.SplitN(safeText(commit.Message), "\n", 2)[0]
	m.patch.preference = m.repo
	m.patch.data, m.patch.err = CommitPatch{}, nil
	m.patch.lines = nil
	m.patch.loading, m.patch.ready = true, false
	m.patch.view = viewport.New()
	m.patch.view.SetWidth(m.viewWidth())
	m.patch.view.SetHeight(max(1, m.height-5))
	m.patch.view.SetContent("Loading patch…")

	// Commands receive a snapshot, so scan results and filters cannot mutate
	// the repository list while a patch is being read in the background.
	paths := slices.Clone(commit.Repositories)
	sort.Strings(paths)
	if index := slices.Index(paths, m.repo); m.repo != "" && index > 0 {
		paths = append([]string{m.repo}, slices.Delete(paths, index, index+1)...)
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.patch.cancel = cancel
	generation, request, hash := m.generation, m.patch.request, commit.Hash
	load := m.loadPatch
	return func() tea.Msg {
		defer cancel()
		data, err := load(ctx, hash, paths)
		return patchMsg{generation: generation, request: request, hash: hash, data: data, err: err}
	}
}

func (m *explorer) receivePatch(msg patchMsg) {
	if msg.generation != m.generation || msg.request != m.patch.request || msg.hash != m.patch.hash || !m.patch.loading {
		return
	}
	if m.patch.cancel != nil {
		m.patch.cancel()
		m.patch.cancel = nil
	}
	m.patch.loading = false
	m.patch.data, m.patch.err = msg.data, msg.err
	m.patch.ready = msg.err == nil
	m.patch.view.SoftWrap = msg.err != nil
	content := "This commit has no file changes."
	if msg.err != nil {
		content = "Could not load this patch.\n\n" + safeText(msg.err.Error()) + "\n\nPress R to retry or Esc to return."
	} else if msg.data.Text != "" {
		content = m.formatPatch(msg.data.Text)
	}
	m.patch.lines = strings.Split(content, "\n")
	m.patch.view.SetContentLines(m.patch.lines)
	m.patch.view.GotoTop()
}

func (m *explorer) stopPatchLoad() {
	if m.patch.cancel != nil {
		m.patch.cancel()
		m.patch.cancel = nil
	}
	m.patch.request++
	m.patch.loading = false
}

func (m *explorer) discardPatch() {
	m.stopPatchLoad()
	m.patch.hash = plumbing.ZeroHash
	m.patch.data, m.patch.err = CommitPatch{}, nil
	m.patch.lines = nil
	m.patch.ready = false
	m.patch.view.SetContent("")
	if m.overlay == "patch" {
		m.overlay = ""
	}
	if m.overlayBack == "patch" {
		m.overlayBack = ""
	}
}

func (m *explorer) syncPatchSelection() {
	if m.patch.hash == plumbing.ZeroHash {
		return
	}
	commit := m.selectedCommit()
	if commit == nil || m.patch.hash != commit.Hash {
		m.discardPatch()
	}
}

func (m *explorer) updatePatch(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "p":
		m.stopPatchLoad()
		m.overlay = ""
	case "R":
		return m.openPatch(true)
	case "?":
		m.openOverlay("help")
		m.overlayBack = "patch"
	case "home":
		m.patch.view.GotoTop()
	case "end":
		m.patch.view.GotoBottom()
	default:
		var cmd tea.Cmd
		m.patch.view, cmd = m.patch.view.Update(msg)
		return cmd
	}
	return nil
}

func (m *explorer) formatPatch(text string) string {
	// Native Git output is plain text. Sanitize file contents just like commit
	// metadata, and expand tabs to make indentation visible when panning.
	text = safeText(strings.ReplaceAll(text, "\t", "    "))
	if !m.color {
		return text
	}
	added := lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	removed := lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	hunk := lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "+"):
			lines[i] = added.Render(line)
		case strings.HasPrefix(line, "-"):
			lines[i] = removed.Render(line)
		case strings.HasPrefix(line, "@@"):
			lines[i] = hunk.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}

func (m *explorer) patchView(width int) string {
	heading := fmt.Sprintf("Patch %s | %s", m.patch.hash.String()[:7], m.patch.subject)
	source := "Loading from local history…"
	status := "Esc cancels loading and returns to your selection."
	if !m.patch.loading {
		source = fmt.Sprintf("Repository: %q", m.patch.data.Repository)
		status = "Changes in this commit"
		if m.patch.data.Merge {
			status = "Merge: compared with first parent"
		}
		if m.patch.data.Truncated {
			status = "Truncated at 1 MiB"
			if m.patch.data.Merge {
				status += " | first parent"
			}
		}
		if m.patch.err != nil {
			source, status = "Patch unavailable", "R retries | Esc returns to your selection"
		}
	}
	keys := "Arrows scroll/pan | PgUp/PgDown page | Home/End"
	back := fmt.Sprintf("Esc/p back | R reload | ? help | q quit | %.0f%%", m.patch.view.ScrollPercent()*100)
	if width < 60 {
		keys, back = "Arrows scroll/pan | PgUp/PgDown", "Esc/p back | R retry | ? | q"
	}
	return strings.Join([]string{heading, source, status, m.patchBody(), keys, back}, "\n")
}

func (m *explorer) patchBody() string {
	if m.patch.loading || m.patch.err != nil {
		return m.patch.view.View()
	}
	view := &m.patch.view
	first := min(view.YOffset(), len(m.patch.lines))
	last := min(first+view.Height(), len(m.patch.lines))
	lines := make([]string, 0, last-first)
	for _, line := range m.patch.lines[first:last] {
		lines = append(lines, clipPatchLine(line, view.XOffset(), view.Width()))
	}
	return fitLines(strings.Join(lines, "\n"), view.Width(), view.Height())
}

// The viewport's ANSI cutter keeps a whole wide glyph across the left edge,
// which can add a column and wrap a diff row. Fill partial glyphs with spaces
// at either edge so every source line occupies exactly one screen row.
func clipPatchLine(line string, left, width int) string {
	if width <= 0 {
		return ""
	}
	right := left + width
	var result strings.Builder
	var state byte
	column := 0
	keepCombining := false
	for len(line) > 0 {
		seq, cells, n, next := ansi.GraphemeWidth.DecodeSequenceInString(line, state, nil)
		switch {
		case cells == 0:
			if strings.HasPrefix(seq, "\x1b") || keepCombining {
				result.WriteString(seq)
			}
		case column >= left && column+cells <= right:
			result.WriteString(seq)
			keepCombining = true
		case column+cells > left && column < right:
			result.WriteString(strings.Repeat(" ", min(column+cells, right)-max(column, left)))
			keepCombining = false
		default:
			keepCombining = false
		}
		column += cells
		state, line = next, line[n:]
	}
	return result.String()
}
