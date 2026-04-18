package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/gookit/color"
)

func buildHeader(start, end time.Time) string {
	s := strings.Builder{}
	s.WriteString("     ") // Offset for day names (Mon, Wed, Fri) which are 5 chars total with padding
	lastMonth := ""
	for current := start; !current.After(end); current = current.AddDate(0, 0, 7) {
		month := current.Month().String()[:3]
		if month != lastMonth {
			s.WriteString(fmt.Sprintf("%-4s", month))
			lastMonth = month
		} else {
			s.WriteString("    ")
		}
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
	}
	return "   "
}

func printTable(commits map[int]int, b Boundary) {
	since := b.Since
	until := b.Until

	for since.Weekday() != time.Sunday {
		since = since.AddDate(0, 0, -1)
	}
	for until.Weekday() != time.Saturday {
		until = until.AddDate(0, 0, 1)
	}

	fmt.Println(buildHeader(since, until))
	max := getMaxValue(commits)

	for i := 0; i < 7; i++ {
		fmt.Printf("%-5s", getDay(i))
		curr := since.AddDate(0, 0, i)
		for !curr.After(until) {
			d := daysAgo(curr)
			fmt.Print(printCell(commits[d], max))
			curr = curr.AddDate(0, 0, 7)
		}
		fmt.Println()
	}

	printLegend(max)
	printSummary(commits, b)
}

func printCell(val, maxValue int) string {
	digitWidth := 1
	if maxValue > 0 {
		digitWidth = len(fmt.Sprintf("%d", maxValue))
	}

	var colorFunc color.Style
	if val == 0 {
		colorFunc = color.New(color.FgLightWhite, color.BgBlack)
		return colorFunc.Sprintf(" %*s ", digitWidth, "-")
	}

	if maxValue <= 0 {
		maxValue = 1
	}

	if val <= maxValue/4 {
		colorFunc = color.New(color.FgBlack, color.BgLightCyan)
	} else if val <= maxValue/2 {
		colorFunc = color.New(color.FgBlack, color.BgHiCyan)
	} else if val <= (maxValue*3)/4 {
		colorFunc = color.New(color.FgBlack, color.BgHiBlue)
	} else {
		colorFunc = color.New(color.FgBlack, color.BgBlue)
	}
	return colorFunc.Sprintf(" %*d ", digitWidth, val)
}

func printLegend(max int) {
	fmt.Printf("\nLegend: Less ")
	fmt.Print(printCell(0, max), " ")
	if max > 0 {
		fmt.Print(printCell(1, max), " ")
	}
	if max > 1 {
		fmt.Print(printCell(max/2, max), " ")
	}
	if max > 2 {
		fmt.Print(printCell(max*3/4, max), " ")
	}
	if max > 3 {
		fmt.Print(printCell(max, max), " ")
	}
	fmt.Println("More")
}

func printSummary(commits map[int]int, b Boundary) {
	total := 0
	for _, v := range commits {
		total += v
	}
	fmt.Printf("Total commits: %s between %s and %s\n",
		color.Green.Sprintf("%d", total),
		color.Cyan.Render(b.Since.Format("2006-01-02")),
		color.Cyan.Render(b.Until.Format("2006-01-02")),
	)
}
