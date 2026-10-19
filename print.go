package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/gookit/color"
)

func cellWidth(max int) int {
	if max < 1 {
		max = 1
	}
	return len(fmt.Sprint(max)) + 2
}

func buildHeader(start, end time.Time, width int) string {
	var s strings.Builder
	s.WriteString("     ")
	lastMonth := time.Month(0)
	for current := start; !current.After(end); current = current.AddDate(0, 0, 7) {
		label := ""
		if current.Month() != lastMonth {
			label = current.Month().String()[:3]
			lastMonth = current.Month()
		}
		fmt.Fprintf(&s, "%-*s", width, label)
	}
	return s.String()
}

func getDay(i int) string {
	switch i {
	case 1:
		return "Mon"
	case 3:
		return "Wed"
	case 5:
		return "Fri"
	default:
		return "   "
	}
}

func printTable(out io.Writer, commits map[string]int, b Boundary, width int) {
	since := b.Since
	until := b.Until.AddDate(0, 0, -1)
	for since.Weekday() != time.Sunday {
		since = since.AddDate(0, 0, -1)
	}
	for until.Weekday() != time.Saturday {
		until = until.AddDate(0, 0, 1)
	}
	maxValue := getMaxValue(commits)
	cell := cellWidth(maxValue)
	availableWeeks := int((until.Unix()-since.Unix())/(7*24*60*60)) + 1
	weeks := max(1, min((width-5)/cell, availableWeeks))

	// Split long ranges into blocks rather than wrapping terminal rows.
	for start := since; !start.After(until); {
		end := start.AddDate(0, 0, weeks*7-1)
		if end.After(until) {
			end = until
		}
		fmt.Fprintf(out, "\n%s – %s\n", start.Format("Jan 2006"), end.Format("Jan 2006"))
		fmt.Fprintln(out, buildHeader(start, end, cell))
		for day := 0; day < 7; day++ {
			fmt.Fprintf(out, "%-5s", getDay(day))
			for current := start.AddDate(0, 0, day); !current.After(end); current = current.AddDate(0, 0, 7) {
				if current.Before(b.Since) || !current.Before(b.Until) {
					fmt.Fprint(out, strings.Repeat(" ", cell))
				} else {
					fmt.Fprint(out, printCell(commits[current.Format(dateLayout)], maxValue))
				}
			}
			fmt.Fprintln(out)
		}
		start = end.AddDate(0, 0, 1)
	}
	printLegend(out, maxValue, width)
	printSummary(out, commits, b)
}

func printCell(val, maxValue int) string {
	digits := cellWidth(maxValue) - 2
	if val == 0 {
		return color.New(color.FgLightWhite, color.BgBlack).Sprintf(" %*s ", digits, "-")
	}
	var style color.Style
	switch contributionLevel(val, maxValue) {
	case 1:
		style = color.New(color.FgBlack, color.BgLightCyan)
	case 2:
		style = color.New(color.FgBlack, color.BgHiCyan)
	case 3:
		style = color.New(color.FgBlack, color.BgHiBlue)
	default:
		style = color.New(color.FgLightWhite, color.BgBlue)
	}
	return style.Sprintf(" %*d ", digits, val)
}

// Both calendars use the same scale for a given set of daily counts.
func contributionLevel(value, maximum int) int {
	switch {
	case value <= 0:
		return 0
	case value <= maximum/4:
		return 1
	case value <= maximum/2:
		return 2
	case value <= maximum/4*3+maximum%4*3/4:
		return 3
	default:
		return 4
	}
}

func printLegend(out io.Writer, maxValue, width int) {
	values := []int{0}
	if maxValue > 0 {
		values = append(values, 1, maxValue/4, maxValue/2, maxValue-maxValue/4, maxValue)
	}
	sort.Ints(values)
	fmt.Fprint(out, "\nLegend: Less ")
	columns := 13
	last := -1
	for _, value := range values {
		if value == last {
			continue
		}
		last = value
		if columns+cellWidth(maxValue)+1 > width {
			fmt.Fprint(out, "\n     ")
			columns = 5
		}
		fmt.Fprint(out, printCell(value, maxValue), " ")
		columns += cellWidth(maxValue) + 1
	}
	if columns+4 > width {
		fmt.Fprint(out, "\n     ")
	}
	fmt.Fprintln(out, "More")
}

func printSummary(out io.Writer, commits map[string]int, b Boundary) {
	total := 0
	for _, count := range commits {
		total += count
	}
	fmt.Fprintf(out, "Total commits: %s between %s and %s\n",
		color.Green.Sprintf("%d", total),
		color.Cyan.Render(b.Since.Format(dateLayout)),
		color.Cyan.Render(b.Until.AddDate(0, 0, -1).Format(dateLayout)),
	)
}
