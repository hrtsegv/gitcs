package main

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Repository metadata is untrusted terminal content. Strip escape sequences and
// controls before rendering it, while retaining paragraphs in commit messages.
func safeText(value string) string {
	value = ansi.Strip(value)
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || r >= '\u202a' && r <= '\u202e' || r >= '\u2066' && r <= '\u2069' {
			return -1
		}
		return r
	}, value)
}

func safeLine(value string) string {
	return strings.Join(strings.Fields(safeText(value)), " ")
}

func (m *explorer) viewWidth() int {
	width := m.width
	if m.widthCap > 0 {
		width = min(width, m.widthCap)
	}
	return max(1, width)
}

func (m *explorer) bodyHeight() int { return max(2, m.height-15) }

func (m *explorer) resize() {
	width := m.viewWidth()
	detailWidth := width
	if width >= 100 {
		detailWidth = width - width/2 - 3
	}
	m.details.SetWidth(max(1, detailWidth))
	m.details.SetHeight(m.bodyHeight() - 1)
	m.overlayView.SetWidth(width)
	m.overlayView.SetHeight(max(1, m.height-2))
	m.search.SetWidth(max(1, width-9))
	m.ensureVisible()
}

func (m *explorer) ensureVisible() {
	rows := m.bodyHeight() - 1
	if m.selected < m.listOffset {
		m.listOffset = m.selected
	}
	if m.selected >= m.listOffset+rows {
		m.listOffset = m.selected - rows + 1
	}
	m.listOffset = max(0, min(m.listOffset, max(0, len(m.visible)-rows)))
}

func (m *explorer) label(text string, focused bool) string {
	if focused {
		text = "[" + text + "]"
	}
	if m.color && focused {
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("2")).Render(text)
	}
	return text
}

func (m *explorer) View() tea.View {
	width, height := m.viewWidth(), max(1, m.height)
	var content string
	switch {
	case width < 32 || height < 20:
		content = fmt.Sprintf("gitcs explore\nResize to at least 32 x 20\nCurrent: %d x %d\nq quit | Ctrl+C cancel", width, height)
	case m.overlay != "":
		if m.overlay == "repositories" {
			content = m.repositoryView(width, height)
		} else {
			content = m.overlayView.View() + "\n\nArrows scroll | Esc back | q quit"
		}
	default:
		authors := strings.Join(m.query.Emails, ", ")
		if authors == "*" {
			authors = "all authors"
		}
		repo := "all repositories"
		if m.repo != "" {
			repo = fmt.Sprintf("%q", m.repo)
		}
		header := fmt.Sprintf("gitcs explore | %q\nAuthors: %s | %s\n%s..%s | %s | branches: %s\n",
			m.root, safeLine(authors), repo,
			m.query.Boundary.Since.Format(dateLayout), m.query.Boundary.Until.AddDate(0, 0, -1).Format(dateLayout),
			m.query.Boundary.Location, m.query.BranchScope)
		footer := "Arrows | Enter open | Tab pane | / search | r repos | ? help | q quit"
		if m.focus == focusDetails {
			footer = "Arrows scroll | PgUp/PgDown page | Esc list | / search | ? help | q quit"
		}
		if width < 70 {
			footer = "Enter open | / search | r repos | ? help | q quit"
		}
		if width < 50 {
			footer = "Enter open | / search | ? | q"
		}
		if m.searching {
			footer = m.search.View()
		} else if m.search.Value() != "" {
			footer = fmt.Sprintf("Search: %s | / edit | Esc on calendar clears | ? help | q quit", safeLine(m.search.Value()))
		}
		content = header + m.calendarView(width) + "\n" + m.bodyView(width) + "\n" + footer + "\n" + m.statusLine()
	}
	if !m.color {
		content = ansi.Strip(content)
	}
	view := tea.NewView(fitLines(content, width, height))
	view.AltScreen = true
	view.WindowTitle = "gitcs explore"
	return view
}

// fitLines uses display columns, rather than bytes, for wide Unicode text.
func fitLines(content string, width, height int) string {
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func civilDays(from, to time.Time) int {
	return int((to.Unix() - from.Unix()) / 86400)
}

func (m *explorer) calendarView(width int) string {
	b := m.query.Boundary
	start := b.Since.AddDate(0, 0, -int(b.Since.Weekday()))
	totalWeeks := (civilDays(start, b.Until) + 6) / 7
	weeks := min(totalWeeks, max(1, (width-4)/3))
	selectedWeek := civilDays(start, m.date) / 7
	firstWeek := max(0, min(selectedWeek-weeks/2, totalWeeks-weeks))
	start = start.AddDate(0, 0, firstWeek*7)
	lines := []string{m.label("Calendar", m.focus == focusCalendar) + "  . 0  : 1  o 2-3  O 4-7  # 8+"}
	months := "    "
	for week := range weeks {
		day := start.AddDate(0, 0, week*7)
		month := "   "
		if week == 0 && day.Month() == day.AddDate(0, 0, 7).Month() || week > 0 && day.Month() != day.AddDate(0, 0, -7).Month() {
			month = day.Format("Jan")
		}
		months += month
	}
	lines = append(lines, months)
	for weekday, name := range []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"} {
		line := name + " "
		for week := range weeks {
			day := start.AddDate(0, 0, week*7+weekday)
			if day.Before(b.Since) || !day.Before(b.Until) {
				line += "   "
				continue
			}
			count := m.days[day.Format(dateLayout)]
			symbol := "."
			switch {
			case count >= 8:
				symbol = "#"
			case count >= 4:
				symbol = "O"
			case count >= 2:
				symbol = "o"
			case count == 1:
				symbol = ":"
			}
			cell := " " + symbol + " "
			if day.Equal(m.date) {
				cell = "[" + symbol + "]"
			}
			if m.color && count > 0 {
				cell = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render(cell)
			}
			line += cell
		}
		lines = append(lines, line)
	}
	lines = append(lines, fmt.Sprintf("%s  |  %d commit(s)  |  weeks %d-%d/%d",
		m.date.Format("Mon 2006-01-02"), m.days[m.date.Format(dateLayout)], firstWeek+1, firstWeek+weeks, totalWeeks))
	return strings.Join(lines, "\n")
}

func (m *explorer) bodyView(width int) string {
	if width >= 100 {
		listWidth := width / 2
		left := m.commitListView(listWidth)
		right := m.label("Details", m.focus == focusDetails) + "\n" + m.details.View()
		divider := strings.TrimSuffix(strings.Repeat(" │ \n", m.bodyHeight()), "\n")
		return lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(listWidth).Render(left), divider, right)
	}
	if m.focus == focusDetails {
		return m.label("Details", true) + "\n" + m.details.View()
	}
	return m.commitListView(width)
}

func (m *explorer) commitListView(width int) string {
	scope := m.date.Format(dateLayout)
	if m.allDates || m.search.Value() != "" {
		scope = "all dates"
	}
	lines := []string{fmt.Sprintf("%s | %s | %d commit(s)", m.label("Commits", m.focus == focusCommits), scope, len(m.visible))}
	if len(m.visible) == 0 {
		message := "No commits on this date. d shows all dates; / searches."
		if m.loading {
			message = "Waiting for matching commits…"
		} else if m.scanErr != nil {
			message = "Scan failed. w shows the error; R retries."
		} else if m.builder.report.Processed == 0 && (len(m.repos) > 0 || len(m.builder.report.Diagnostics) > 0) {
			message = "No repositories could be read. Press w for warnings."
		} else if len(m.repos) == 0 {
			message = "No Git repositories found. Check -path DIRECTORY."
		} else if len(m.days) == 0 {
			message = "No matches. Clear search, change repo, or adjust CLI filters."
		}
		lines = append(lines, message)
	}
	for i := m.listOffset; i < len(m.visible) && len(lines) < m.bodyHeight(); i++ {
		commit := m.builder.report.Commits[m.visible[i]]
		prefix := "  "
		if i == m.selected {
			prefix = "> "
		}
		subject := strings.SplitN(safeText(commit.Message), "\n", 2)[0]
		if subject == "" {
			subject = "(no subject)"
		}
		line := fmt.Sprintf("%s%s %s %s", prefix, commit.Hash.String()[:7], commit.AuthorWhen.In(m.query.Boundary.Location).Format("01-02"), subject)
		lines = append(lines, ansi.Truncate(line, width, "…"))
	}
	return fitLines(strings.Join(lines, "\n"), width, m.bodyHeight())
}

func (m *explorer) repositoryView(width, height int) string {
	lines := []string{"Filter by repository", ""}
	rows := max(1, height-4)
	first := max(0, min(m.repoCursor-rows/2, len(m.repos)+1-rows))
	for i := first; i <= len(m.repos) && i < first+rows; i++ {
		label := "All repositories"
		if i > 0 {
			label = fmt.Sprintf("%q", m.repos[i-1])
		}
		prefix := "  "
		if i == m.repoCursor {
			prefix = "> "
		}
		lines = append(lines, prefix+label)
	}
	lines = append(lines, "", "Arrows choose | Enter apply | Esc back | q quit")
	return fitLines(strings.Join(lines, "\n"), width, height)
}

func (m *explorer) statusLine() string {
	if m.discovery {
		return "Discovering repositories… | R restart | w warnings"
	}
	warnings := len(m.builder.report.Diagnostics)
	status := "Ready"
	if m.loading {
		status = "Loading"
	} else if m.scanErr != nil || m.builder.report.Processed == 0 && (len(m.repos) > 0 || warnings > 0) {
		status = "Failed"
	} else if warnings > 0 {
		status = "Partial results"
	}
	total := 0
	for _, count := range m.days {
		total += count
	}
	if m.viewWidth() < 70 {
		return fmt.Sprintf("%s %d/%d | %d commits | w:%d", status, m.completed, len(m.repos), total, warnings)
	}
	return fmt.Sprintf("%s | %d warnings (w) | repos %d/%d read, %d finished | %d commits | R refresh",
		status, warnings, m.builder.report.Processed, len(m.repos), m.completed, total)
}
