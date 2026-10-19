package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func calendarRows(t *testing.T, content string) []string {
	t.Helper()
	lines := strings.Split(ansi.Strip(content), "\n")
	if len(lines) != 10 {
		t.Fatalf("calendar should reserve ten rows, got %d: %q", len(lines), content)
	}
	for i, weekday := range []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"} {
		if !strings.HasPrefix(lines[i+2], weekday+" ") {
			t.Fatalf("weekday row %d: %q", i, lines[i+2])
		}
	}
	return lines[2:9]
}

func TestExplorerCalendarDisplaysExactDailyCounts(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newExplorer(context.Background(), "/workspace", testQuery(t, "2026-10-04", "2026-10-10", "UTC"), 0)
	t.Cleanup(m.cancel)
	if m.color {
		t.Fatal("NO_COLOR did not disable styles")
	}
	m.loading = false
	counts := []int{0, 1, 9, 10, 99, 100, 10000}
	for i, count := range counts {
		m.days[m.query.Boundary.Since.AddDate(0, 0, i).Format(dateLayout)] = count
	}
	for selected, count := range counts {
		m.date = m.query.Boundary.Since.AddDate(0, 0, selected)
		view := m.calendarView(80)
		if strings.Contains(view, "\x1b") {
			t.Fatalf("NO_COLOR calendar contains an escape: %q", view)
		}
		rows := calendarRows(t, view)
		for i, row := range rows {
			value := "-"
			if counts[i] > 0 {
				value = fmt.Sprint(counts[i])
			}
			cell := strings.TrimSpace(row[4:])
			if i == selected {
				pattern := `^\[\s*` + regexp.QuoteMeta(value) + `\]$`
				if !regexp.MustCompile(pattern).MatchString(cell) {
					t.Fatalf("selected count %d lost its brackets: %q", counts[i], cell)
				}
			} else if cell != value {
				t.Fatalf("count %d rendered as %q", counts[i], cell)
			}
		}
		label := fmt.Sprintf("%d commits", count)
		if count == 1 {
			label = "1 commit"
		}
		if !strings.Contains(view, m.date.Format("Mon 2006-01-02")+" | "+label) {
			t.Fatalf("selected date/count is unclear: %s", view)
		}
		m.color = true
		if colored := ansi.Strip(m.calendarView(80)); colored != view {
			t.Fatalf("color changes calendar data or selection:\nplain: %q\ncolor: %q", view, colored)
		}
		m.color = false
	}
	// The minimum supported terminal must still fit the exact selected value.
	m.date = m.query.Boundary.Until.AddDate(0, 0, -1)
	narrow := m.calendarView(32)
	if !strings.Contains(strings.Join(calendarRows(t, narrow), "\n"), "[10000]") {
		t.Fatalf("narrow calendar clipped a selected high count: %s", narrow)
	}
	for _, line := range strings.Split(narrow, "\n") {
		if ansi.StringWidth(line) > 32 {
			t.Fatalf("narrow high-count calendar overflows: %q", line)
		}
	}
}

func TestExplorerCalendarKeepsCellWidthAcrossHiddenHighCounts(t *testing.T) {
	m := testExplorer(t)
	m.query = testQuery(t, "2026-01-04", "2026-06-30", "UTC")
	m.days = map[string]int{"2026-01-04": 1, "2026-06-28": 10000}
	m.loading = false
	m.date = m.query.Boundary.Since
	first := m.calendarView(80)
	firstRows := calendarRows(t, first)
	if strings.Contains(strings.Join(firstRows, "\n"), "10000") {
		t.Fatal("fixture's high count should be outside the initial visible weeks")
	}
	m.date = m.query.Boundary.Until.AddDate(0, 0, -1)
	lastRows := calendarRows(t, m.calendarView(80))
	if !strings.Contains(strings.Join(lastRows, "\n"), "10000") {
		t.Fatal("scrolling to the end did not reveal the high count")
	}
	for i := range firstRows {
		if ansi.StringWidth(firstRows[i]) != ansi.StringWidth(lastRows[i]) {
			t.Fatalf("calendar columns changed width while scrolling: %q / %q", firstRows[i], lastRows[i])
		}
		// A five-digit value and its selection brackets need seven columns.
		// Even invisible high-count days must reserve that space from the start.
		if got := ansi.StringWidth(firstRows[i]); got != 4+10*7 {
			t.Fatalf("high count outside the window did not reserve space: %d columns", got)
		}
	}
}

func TestExplorerCalendarLabelsPartialWeeksAndYearChanges(t *testing.T) {
	for _, tt := range []struct {
		name, since, until string
		labels             []string
	}{
		{"partial first week", "2026-04-30", "2026-05-31", []string{"Apr", "May"}},
		{"year change", "2025-12-25", "2026-01-31", []string{"Dec", "Jan 2026"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := testExplorer(t)
			m.query = testQuery(t, tt.since, tt.until, "UTC")
			m.date = m.query.Boundary.Since
			monthLine := strings.Split(ansi.Strip(m.calendarView(80)), "\n")[1]
			for _, label := range tt.labels {
				if !strings.Contains(monthLine, label) {
					t.Fatalf("missing month label %q in %q", label, monthLine)
				}
			}
		})
	}
	m := testExplorer(t)
	m.query = testQuery(t, "2025-01-01", "2025-12-31", "UTC")
	m.date = m.query.Boundary.Since
	first := strings.Split(ansi.Strip(m.calendarView(32)), "\n")[1]
	if !strings.Contains(first, "Jan") || strings.Contains(first, "Dec") {
		t.Fatalf("initial narrow window: %q", first)
	}
	m.date = m.query.Boundary.Until.AddDate(0, 0, -1)
	last := m.calendarView(32)
	months := strings.Split(ansi.Strip(last), "\n")[1]
	if !strings.Contains(months, "Dec") || strings.Contains(months, "Jan") {
		t.Fatalf("final narrow window: %q", months)
	}
	if strings.Count(strings.Join(calendarRows(t, last), "\n"), "[-]") != 1 {
		t.Fatal("narrow window lost or duplicated selected date")
	}
}

func TestExplorerCalendarMonthLabelsStaySeparated(t *testing.T) {
	for _, tt := range []struct {
		name, since, until string
		maximum, width     int
	}{
		{"single narrow week", "2026-04-30", "2026-05-31", 1, 32},
		{"full report range", "2026-04-20", "2026-10-16", 39, 187},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := testExplorer(t)
			m.query = testQuery(t, tt.since, tt.until, "UTC")
			m.date = m.query.Boundary.Since
			m.days = map[string]int{tt.since: tt.maximum}
			months := strings.Split(ansi.Strip(m.calendarView(tt.width)), "\n")[1]
			if !regexp.MustCompile(`Apr(?:\s+2026)?\s+May`).MatchString(months) {
				t.Fatalf("month labels touch or overlap: %q", months)
			}
			if ansi.StringWidth(months) > tt.width {
				t.Fatalf("month labels overflow the calendar: %q", months)
			}
		})
	}
}

func TestExplorerCountLabelsDistinguishTotalFiltersAndDay(t *testing.T) {
	m := testExplorer(t)
	loadExplorer(m)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	assertLabels := func(want ...string) {
		t.Helper()
		view := m.View().Content
		for _, label := range want {
			if !strings.Contains(view, label) {
				t.Fatalf("missing %q in:\n%s", label, view)
			}
		}
	}
	assertLabels("Total: 3", "Fri 2026-10-16 | 2 commits", "2 on 2026-10-16")
	if strings.Contains(m.View().Content, "Matching:") {
		t.Fatal("unfiltered view should not present an extra matching total")
	}
	m.repo = "/two"
	m.refreshSelection(false)
	assertLabels("Total: 3", "Matching: 1", "Fri 2026-10-16 | 1 commit", "1 on 2026-10-16")
	m.repo = ""
	m.search.SetValue("older")
	m.refreshSelection(false)
	assertLabels("Total: 3", "Matching: 1", "1 across all dates")
	m.search.SetValue("does not exist")
	m.refreshSelection(false)
	assertLabels("Total: 3", "Matching: 0", "0 across all dates", "No matches.")
	m.search.SetValue("")
	m.allDates = true
	m.refreshSelection(false)
	assertLabels("Total: 3", "3 across all dates")
}

func TestExplorerMainViewResponsiveAndSafe(t *testing.T) {
	m := testExplorer(t)
	loadExplorer(m)
	m.root = "/workspace/" + strings.Repeat("界🙂", 60)
	m.selectedCommit().Message = "界🙂 subject\x1b[31m red\x1b[0m\n\x1b]52;c;secret\a\nbody"
	m.updateDetails(true)
	for _, size := range [][2]int{{32, 20}, {50, 24}, {60, 24}, {80, 24}, {187, 43}} {
		for _, color := range []bool{false, true} {
			m.color = color
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			for _, focus := range []explorerFocus{focusCalendar, focusCommits, focusDetails} {
				m.focus = focus
				view := m.View()
				plain := ansi.Strip(view.Content)
				if !view.AltScreen || strings.Contains(view.Content, "\x1b]52") || strings.Contains(plain, "secret") {
					t.Fatalf("unsafe view at %v: %q", size, view.Content)
				}
				if !color && strings.Contains(view.Content, "\x1b") {
					t.Fatalf("plain view has ANSI at %v", size)
				}
				lines := strings.Split(plain, "\n")
				if len(lines) != size[1] {
					t.Fatalf("height at %v: %d", size, len(lines))
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > size[0] {
						t.Fatalf("line exceeds %v: %q", size, line)
					}
				}
				if !strings.Contains(lines[0], "Total: 3") || !strings.Contains(lines[size[1]-2], "q") {
					t.Fatalf("total or quit control clipped at %v: %s", size, plain)
				}
				if size[0] < 70 && focus != focusCalendar && !strings.Contains(lines[size[1]-2], "Esc") {
					t.Fatalf("back control missing from compact pane footer: %q", lines[size[1]-2])
				}
				if !strings.Contains(lines[size[1]-1], "Ready") {
					t.Fatalf("status footer missing at %v: %q", size, lines[size[1]-1])
				}
			}
		}
	}
}

func TestExplorerResizeKeepsListAndDetailScrollValid(t *testing.T) {
	m := testExplorer(t)
	m.discovery, m.loading = false, false
	var commits []CommitRecord
	for i := 1; i <= 20; i++ {
		commits = append(commits, explorerRecord(i, fmt.Sprintf("change %d", i), "2026-10-16T12:00:00Z"))
	}
	m.builder.add(repositoryResult{path: "/one", commits: commits})
	m.refreshSelection(false)
	m.Update(tea.WindowSizeMsg{Width: 32, Height: 20})
	m.focus = focusCommits
	press(m, tea.KeyEnd)
	selected := m.selectedCommit().Hash
	if m.listOffset == 0 {
		t.Fatal("fixture did not scroll the list")
	}
	m.details.SetContent(strings.Repeat("detail line\n", 20) + "DETAIL_END")
	m.details.GotoBottom()
	if m.details.YOffset() == 0 {
		t.Fatal("fixture did not scroll details")
	}
	m.Update(tea.WindowSizeMsg{Width: 187, Height: 43})
	if m.selectedCommit().Hash != selected || m.listOffset != 0 {
		t.Fatal("expanding terminal lost selection or left list unnecessarily scrolled")
	}
	if m.details.YOffset() != 0 || !strings.Contains(m.details.View(), "DETAIL_END") {
		t.Fatalf("expanded detail viewport kept an invalid offset: %d", m.details.YOffset())
	}
}

func TestExplorerWideCommitRowsShowReportingTime(t *testing.T) {
	m := testExplorer(t)
	loadExplorer(m)
	m.query.Boundary.Location = time.FixedZone("UTC+03", 3*60*60)
	wide := m.commitListView(100)
	if !strings.Contains(wide, "10-16 15:00") || !strings.Contains(wide, "newest change") {
		t.Fatalf("wide list lacks local author time or subject: %s", wide)
	}
	if strings.Contains(m.commitListView(40), "15:00") {
		t.Fatal("narrow list should reserve space for the commit subject")
	}
}
