package main

import (
	"fmt"
	"net/mail"
	"os/exec"
	"strings"
	"time"
)

const dateLayout = "2006-01-02"
const sixMonthsInDays = 182

// Boundary is a half-open interval of civil calendar dates. Dates are stored
// at UTC midnight for arithmetic; commit timestamps are bucketed in Location.
// Until is the day AFTER the inclusive end date shown to users.
type Boundary struct {
	Since        time.Time
	Until        time.Time
	Location     *time.Location
	UntilClamped bool
}

func calendarDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func dateKey(t time.Time, location *time.Location) string {
	return t.In(location).Format(dateLayout)
}

func setTimeFlags(since, until string, now time.Time) (Boundary, error) {
	end := calendarDate(now)
	b := Boundary{Location: now.Location()}
	if until != "" {
		parsed, err := time.Parse(dateLayout, until)
		if err != nil {
			return b, fmt.Errorf("invalid 'until' date %q; use YYYY-MM-DD", until)
		}
		if parsed.After(end) {
			b.UntilClamped = true
		} else {
			end = parsed
		}
	}
	b.Since = end.AddDate(0, 0, -sixMonthsInDays)
	if since != "" {
		parsed, err := time.Parse(dateLayout, since)
		if err != nil {
			return b, fmt.Errorf("invalid 'since' date %q; use YYYY-MM-DD", since)
		}
		b.Since = parsed
	}
	if b.Since.After(end) {
		return b, fmt.Errorf("'since' must be on or before 'until' (%s)", end.Format(dateLayout))
	}
	b.Until = end.AddDate(0, 0, 1)
	return b, nil
}

func getGitEmail(path string) string {
	// Git resolves repository-local, includeIf, global, and system configuration.
	output, err := exec.Command("git", "-C", path, "config", "--get", "user.email").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func parseEmails(values []string) ([]string, error) {
	var emails []string
	seen := make(map[string]bool)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "*" {
			if len(values) != 1 {
				return nil, fmt.Errorf("-email '*' cannot be combined with other authors")
			}
			return []string{"*"}, nil
		}
		address, err := mail.ParseAddress(value)
		if err != nil {
			return nil, fmt.Errorf("invalid email address %q; use -email name@example.com or -email '*'", value)
		}
		if !seen[address.Address] {
			emails = append(emails, address.Address)
			seen[address.Address] = true
		}
	}
	return emails, nil
}

func getMaxValue(m map[string]int) int {
	max := 0
	for _, v := range m {
		if v > max {
			max = v
		}
	}
	return max
}
