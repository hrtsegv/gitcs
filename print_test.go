package main

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestVeryLargeWidth(t *testing.T) {
	q := testQuery(t, "2026-10-10", "2026-10-10", "UTC")
	var out bytes.Buffer
	printTable(&out, map[string]int{"2026-10-10": 1}, q.Boundary, math.MaxInt)
	if !strings.Contains(plain(out.String()), "Total commits: 1") {
		t.Fatalf("wide calendar did not finish: %s", out.String())
	}
}

func TestCalendarAlignmentAndWidth(t *testing.T) {
	for _, maxValue := range []int{1, 10, 100} {
		for _, width := range []int{20, 40, 80, 120} {
			t.Run(fmt.Sprintf("count_%d_width_%d", maxValue, width), func(t *testing.T) {
				q := testQuery(t, "2025-10-16", "2026-10-16", "UTC")
				var out bytes.Buffer
				printTable(&out, map[string]int{"2026-10-10": maxValue}, q.Boundary, width)
				lines := strings.Split(plain(out.String()), "\n")
				headers := 0
				for i, line := range lines {
					// A calendar header starts with five spaces and a month name.
					if len(line) < 8 || !strings.HasPrefix(line, "     ") || !strings.Contains("JanFebMarAprMayJunJulAugSepOctNovDec", line[5:8]) || strings.TrimSpace(line[5:8]) == "" {
						continue
					}
					headers++
					if len(line) > width {
						t.Fatalf("header width %d exceeds %d: %q", len(line), width, line)
					}
					for day := 1; day <= 7; day++ {
						if len(lines[i+day]) != len(line) {
							t.Fatalf("header width %d, row width %d: %q", len(line), len(lines[i+day]), lines[i+day])
						}
					}
				}
				if headers < 2 {
					t.Fatalf("expected range to split into blocks, got %d headers", headers)
				}
				if !strings.Contains(out.String(), "Total commits:") {
					t.Fatal("missing summary")
				}
			})
		}
	}
}

func TestCalendarMasksOutsideRange(t *testing.T) {
	q := testQuery(t, "2026-10-01", "2026-10-01", "UTC")
	var out bytes.Buffer
	printTable(&out, map[string]int{"2026-10-01": 1}, q.Boundary, 80)
	lines := strings.Split(plain(out.String()), "\n")
	if strings.TrimSpace(lines[3]) != "" {
		t.Fatalf("Sunday outside range should be blank: %q", lines[3])
	}
	if strings.Contains(lines[4], "-") {
		t.Fatalf("Monday outside range shown as zero: %q", lines[4])
	}
	if !strings.Contains(lines[7], "1") {
		t.Fatalf("Thursday count missing: %q", lines[7])
	}
	if strings.Contains(lines[8], "-") {
		t.Fatalf("Friday outside range shown as zero: %q", lines[8])
	}
	if !strings.Contains(plain(out.String()), "between 2026-10-01 and 2026-10-01") {
		t.Fatalf("exclusive end leaked into summary: %s", out.String())
	}
}

func TestCalendarYearLabelsAndSummary(t *testing.T) {
	q := testQuery(t, "2025-12-31", "2026-01-01", "UTC")
	var out bytes.Buffer
	printTable(&out, map[string]int{"2025-12-31": 3, "2026-01-01": 7}, q.Boundary, 80)
	text := plain(out.String())
	for _, want := range []string{"Dec 2025", "Jan 2026", "Total commits: 10 between 2025-12-31 and 2026-01-01"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
}

func TestCellAndLegendSmallCounts(t *testing.T) {
	for _, maxValue := range []int{0, 1, 2, 3, 10, 100} {
		for value := 0; value <= maxValue; value++ {
			if cell := plain(printCell(value, maxValue)); len(cell) != cellWidth(maxValue) {
				t.Fatalf("count %d/%d has inconsistent width: %q", value, maxValue, cell)
			}
		}
		var out bytes.Buffer
		printLegend(&out, maxValue, 40)
		text := plain(out.String())
		if maxValue > 0 && !strings.Contains(text, fmt.Sprint(maxValue)) {
			t.Fatalf("legend omitted maximum %d: %s", maxValue, text)
		}
		for _, line := range strings.Split(text, "\n") {
			if len(line) > 40 {
				t.Fatalf("legend exceeded width: %q", line)
			}
		}
	}
}
