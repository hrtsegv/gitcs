package main

import (
	"fmt"
	"sort"
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
	m.details.SetYOffset(m.details.YOffset())
	m.overlayView.SetWidth(width)
	m.overlayView.SetHeight(max(1, m.height-2))
	m.overlayView.SetYOffset(m.overlayView.YOffset())
	m.patch.view.SetWidth(width)
	m.patch.view.SetHeight(max(1, m.height-5))
	m.patch.view.SetXOffset(m.patch.view.XOffset())
	m.patch.view.SetYOffset(m.patch.view.YOffset())
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
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")).Render(text)
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
		} else if m.overlay == "patch" {
			content = m.patchView(width)
		} else {
			content = m.overlayView.View() + "\n\nArrows scroll | Esc back | q quit"
		}
	default:
		footer := "Arrows move | Enter open | p patch | d all dates | / search | r repos | ? help | q quit"
		if width < 100 {
			footer = "Arrows | Enter open | p patch | d all dates | / search | ? | q"
		}
		if m.focus == focusDetails {
			footer = "Arrows scroll | p patch | Esc list | / search | ? help | q quit"
		}
		if width < 70 {
			footer = "Enter open | p patch | d dates | / search | ? | q"
		}
		if width < 50 {
			footer = "Enter | p patch | d | / | ? | q"
			if m.focus == focusDetails {
				footer = "Arrows scroll | Esc | p | ? | q"
			} else if m.focus == focusCommits {
				footer = "Arrows | Enter | Esc | p | ? | q"
			}
		}
		if m.searching {
			footer = m.search.View()
		} else if m.search.Value() != "" {
			footer = spreadLine("Search: "+safeLine(m.search.Value()), "/ edit | Esc back/clear | ? | q", width)
		}
		content = m.headerView(width) + "\n" + m.calendarView(width) + "\n" + m.bodyView(width) + "\n" + footer + "\n" + m.statusLine()
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

// Keep the right-hand summary visible even when a path or label is long.
func spreadLine(left, right string, width int) string {
	left = ansi.Truncate(left, max(0, width-ansi.StringWidth(right)-1), "…")
	return left + strings.Repeat(" ", max(1, width-ansi.StringWidth(left)-ansi.StringWidth(right))) + right
}

func (m *explorer) muted(text string) string {
	if m.color {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(text)
	}
	return text
}

func (m *explorer) sectionLine(title, meta string, focused bool, width int) string {
	title = m.label(title, focused)
	gap := width - ansi.StringWidth(title) - ansi.StringWidth(meta) - 2
	if gap < 1 {
		return spreadLine(title, meta, width)
	}
	return title + " " + m.muted(strings.Repeat("─", gap)) + " " + meta
}

func (m *explorer) matchingCount() int {
	total := 0
	for _, count := range m.days {
		total += count
	}
	return total
}

func (m *explorer) headerView(width int) string {
	title := "gitcs explore"
	total := fmt.Sprintf("Total: %d", len(m.builder.report.Commits))
	if m.color {
		title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")).Render(title)
		total = lipgloss.NewStyle().Bold(true).Render(total)
	}
	if width >= 70 {
		title += " | " + m.muted(fmt.Sprintf("%q", m.root))
	}
	authors := safeLine(strings.Join(m.query.Emails, ", "))
	if authors == "*" {
		authors = "all authors"
	}
	repo := "all repositories"
	if m.repo != "" {
		repo = fmt.Sprintf("%q", m.repo)
	}
	filters := fmt.Sprintf("Authors: %s | %s", authors, repo)
	if m.repo != "" || m.search.Value() != "" {
		filters = fmt.Sprintf("Matching: %d | %s", m.matchingCount(), filters)
	}
	scope := "all local branches + HEAD"
	if m.query.BranchScope == HeadBranch {
		scope = "HEAD history"
	}
	dates := fmt.Sprintf("%s..%s | %s | %s | author dates",
		m.query.Boundary.Since.Format(dateLayout), m.query.Boundary.Until.AddDate(0, 0, -1).Format(dateLayout),
		m.query.Boundary.Location, scope)
	return strings.Join([]string{spreadLine(title, total, width), filters, dates}, "\n")
}

func commitLabel(count int) string {
	if count == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", count)
}

func (m *explorer) commitDetails(commit CommitRecord) string {
	message := strings.SplitN(safeText(commit.Message), "\n", 2)
	subject := message[0]
	if subject == "" {
		subject = "(no subject)"
	}
	if m.color {
		subject = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")).Render(subject)
	}
	parts := []string{subject, m.muted(commit.Hash.String()), "",
		fmt.Sprintf("Author: %s <%s>", safeLine(commit.AuthorName), safeLine(commit.AuthorEmail)),
		"Authored: " + commit.AuthorWhen.In(m.query.Boundary.Location).Format(time.RFC3339),
		"Committed: " + commit.CommitterWhen.In(m.query.Boundary.Location).Format(time.RFC3339),
	}
	if len(message) > 1 && strings.TrimSpace(message[1]) != "" {
		parts = append(parts, "", strings.Trim(message[1], "\n"))
	}
	paths := make([]string, 0, len(commit.Repositories))
	for _, path := range commit.Repositories {
		paths = append(paths, fmt.Sprintf("  %q", path))
	}
	sort.Strings(paths)
	parts = append(parts, "", "Repositories:", strings.Join(paths, "\n"))
	return strings.Join(parts, "\n")
}

func civilDays(from, to time.Time) int {
	return int((to.Unix() - from.Unix()) / 86400)
}

func (m *explorer) calendarView(width int) string {
	b := m.query.Boundary
	start := b.Since.AddDate(0, 0, -int(b.Since.Weekday()))
	maximum := getMaxValue(m.days)
	cell := cellWidth(maximum)
	totalWeeks := (civilDays(start, b.Until) + 6) / 7
	weeks := min(totalWeeks, max(1, (width-4)/cell))
	selectedWeek := civilDays(start, m.date) / 7
	firstWeek := max(0, min(selectedWeek-weeks/2, totalWeeks-weeks))
	start = start.AddDate(0, 0, firstWeek*7)
	meta := fmt.Sprintf("daily counts | peak %d", maximum)
	if width < 50 {
		meta = "daily counts"
	}
	if m.loading {
		meta = "counts so far"
	}
	lines := []string{m.sectionLine("Calendar", meta, m.focus == focusCalendar, width)}
	monthLabels := make([]string, 4+weeks*cell)
	type monthLabel struct {
		at  int
		day time.Time
	}
	var labels []monthLabel
	for week := range weeks {
		day := start.AddDate(0, 0, week*7)
		if day.Before(b.Since) {
			day = b.Since
		}
		previous := start.AddDate(0, 0, (week-1)*7)
		if previous.Before(b.Since) {
			previous = b.Since
		}
		if week == 0 || day.Month() != previous.Month() {
			labels = append(labels, monthLabel{4 + week*cell, day})
		}
	}
	for i, label := range labels {
		end := len(monthLabels)
		if i+1 < len(labels) {
			end = labels[i+1].at
		}
		// Use one column of the weekday gutter when the first partial month
		// occupies a single narrow cell, leaving a gap before the next label.
		if i == 0 && i+1 < len(labels) && end-label.at <= 3 {
			label.at--
		}
		text := label.day.Format("Jan")
		if (i == 0 || label.day.Month() == time.January) && end-label.at > len("Jan 2006") {
			text = label.day.Format("Jan 2006")
		}
		for j, r := range text {
			if label.at+j >= end {
				break
			}
			monthLabels[label.at+j] = string(r)
		}
	}
	for i := range monthLabels {
		if monthLabels[i] == "" {
			monthLabels[i] = " "
		}
	}
	lines = append(lines, m.muted(strings.Join(monthLabels, "")))
	for weekday, name := range []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"} {
		line := name + " "
		for week := range weeks {
			day := start.AddDate(0, 0, week*7+weekday)
			if day.Before(b.Since) || !day.Before(b.Until) {
				line += strings.Repeat(" ", cell)
				continue
			}
			count := m.days[day.Format(dateLayout)]
			line += m.calendarCell(count, maximum, day.Equal(m.date))
		}
		lines = append(lines, line)
	}
	selected := fmt.Sprintf("%s | %s", m.date.Format("Mon 2006-01-02"), commitLabel(m.days[m.date.Format(dateLayout)]))
	if width >= 70 {
		selected = spreadLine(selected, fmt.Sprintf("weeks %d-%d/%d", firstWeek+1, firstWeek+weeks, totalWeeks), width)
	}
	lines = append(lines, selected)
	return strings.Join(lines, "\n")
}

func (m *explorer) calendarCell(count, maximum int, selected bool) string {
	value := "-"
	if count > 0 {
		value = fmt.Sprint(count)
	}
	digits := cellWidth(maximum) - 2
	cell := fmt.Sprintf(" %*s ", digits, value)
	if selected {
		cell = fmt.Sprintf("[%*s]", digits, value)
	}
	if !m.color {
		return cell
	}
	style := lipgloss.NewStyle()
	if selected {
		style = style.Foreground(lipgloss.Color("0")).Background(lipgloss.Color("11")).Bold(true)
	} else {
		switch contributionLevel(count, maximum) {
		case 0:
			return m.muted(cell)
		case 1:
			style = style.Foreground(lipgloss.Color("0")).Background(lipgloss.Color("14"))
		case 2:
			style = style.Foreground(lipgloss.Color("0")).Background(lipgloss.Color("6"))
		case 3:
			style = style.Foreground(lipgloss.Color("15")).Background(lipgloss.Color("12"))
		default:
			style = style.Foreground(lipgloss.Color("15")).Background(lipgloss.Color("4"))
		}
	}
	return style.Render(cell)
}

func (m *explorer) bodyView(width int) string {
	if width >= 100 {
		listWidth := width / 2
		left := m.commitListView(listWidth)
		right := m.sectionLine("Details", "", m.focus == focusDetails, width-listWidth-3) + "\n" + m.details.View()
		divider := strings.TrimSuffix(strings.Repeat(" │ \n", m.bodyHeight()), "\n")
		return lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(listWidth).Render(left), divider, right)
	}
	if m.focus == focusDetails {
		return m.sectionLine("Details", "", true, width) + "\n" + m.details.View()
	}
	return m.commitListView(width)
}

func (m *explorer) commitListView(width int) string {
	scope := fmt.Sprintf("%d on %s", len(m.visible), m.date.Format(dateLayout))
	if m.allDates || m.search.Value() != "" {
		scope = fmt.Sprintf("%d across all dates", len(m.visible))
	}
	lines := []string{m.sectionLine("Commits", scope, m.focus == focusCommits, width)}
	if len(m.visible) == 0 {
		message := "No commits on this date. d shows all dates; / searches."
		if m.loading {
			message = "Waiting for matching commits…"
		} else if m.search.Value() != "" {
			message = "No matches. / edits the search; Esc on calendar clears."
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
		stamp := commit.AuthorWhen.In(m.query.Boundary.Location).Format("01-02")
		if width >= 90 {
			stamp = commit.AuthorWhen.In(m.query.Boundary.Location).Format("01-02 15:04")
		}
		line := fmt.Sprintf("%s%s %s %s", prefix, commit.Hash.String()[:7], stamp, subject)
		line = ansi.Truncate(line, width, "…")
		if i == m.selected && m.color {
			line = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("4")).Bold(true).Width(width).Render(line)
		}
		lines = append(lines, line)
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
	if m.viewWidth() < 70 {
		if status == "Partial results" {
			status = "Partial"
		}
		return fmt.Sprintf("%s | %d/%d repos | w:%d", status, m.completed, len(m.repos), warnings)
	}
	progress := fmt.Sprintf("%d/%d repositories read", m.builder.report.Processed, len(m.repos))
	if m.loading {
		progress = fmt.Sprintf("%d/%d repositories finished | counts so far", m.completed, len(m.repos))
	}
	return fmt.Sprintf("%s | %s | %d warnings (w) | R refresh", status, progress, warnings)
}
