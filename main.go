package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/briandowns/spinner"
	"golang.org/x/term"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, time.Now())
	stop()
	os.Exit(code)
}

type emailFlags []string

func (e *emailFlags) String() string { return strings.Join(*e, ", ") }
func (e *emailFlags) Set(value string) error {
	*e = append(*e, value)
	return nil
}

func run(ctx context.Context, args []string, out, errOut io.Writer, now time.Time) int {
	flags := flag.NewFlagSet("gitcs", flag.ContinueOnError)
	flags.SetOutput(errOut)
	var since, until, path, scope, timezone string
	var emails emailFlags
	var width int
	flags.StringVar(&path, "path", ".", "directory to scan recursively")
	flags.Var(&emails, "email", "author email; repeat for multiple identities, or '*' for all authors (default: Git configuration)")
	flags.StringVar(&since, "since", "", "inclusive start date, YYYY-MM-DD (default: 182 days before end date)")
	flags.StringVar(&until, "until", "", "inclusive end date, YYYY-MM-DD (default: today)")
	flags.StringVar(&scope, "branch-scope", string(LocalBranches), "history to count: local (all local branches plus HEAD) or head")
	flags.StringVar(&timezone, "timezone", "Local", "reporting timezone, e.g. UTC or Asia/Beirut")
	flags.IntVar(&width, "width", 0, "calendar width in columns (default: terminal width, or 80 when piped)")
	flags.Usage = func() {
		fmt.Fprintln(errOut, "Usage: gitcs [flags]")
		fmt.Fprintln(errOut, "\nVisualize unique local commits by author date.")
		fmt.Fprintln(errOut, "\nExamples:")
		fmt.Fprintln(errOut, "  gitcs")
		fmt.Fprintln(errOut, "  gitcs -path ~/dev -email '*'")
		fmt.Fprintln(errOut, "  gitcs -since 2026-01-01 -until 2026-01-31 -timezone UTC")
		fmt.Fprintln(errOut, "\nFlags:")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(errOut, "gitcs: %s\n", err)
		if errors.Is(err, context.Canceled) {
			return 130
		}
		return 1
	}
	if flags.NArg() != 0 {
		return fail(fmt.Errorf("unexpected arguments %q; use -path DIRECTORY", flags.Args()))
	}
	if width != 0 && width < 20 {
		return fail(fmt.Errorf("-width must be at least 20 columns"))
	}
	if BranchScope(scope) != LocalBranches && BranchScope(scope) != HeadBranch {
		return fail(fmt.Errorf("invalid branch scope %q; use local or head", scope))
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return fail(fmt.Errorf("unknown timezone %q; use an IANA name such as UTC or Asia/Beirut", timezone))
	}
	now = now.In(location)
	boundary, err := setTimeFlags(since, until, now)
	if err != nil {
		return fail(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fail(fmt.Errorf("cannot scan %q: %w", path, err))
	}
	if !info.IsDir() {
		return fail(fmt.Errorf("path %q is not a directory", path))
	}
	if len(emails) == 0 {
		email := getGitEmail(path)
		if email == "" {
			return fail(fmt.Errorf("no Git email configured; use -email name@example.com or -email '*' for all authors"))
		}
		emails = append(emails, email)
	}
	authors, err := parseEmails(emails)
	if err != nil {
		return fail(err)
	}
	if boundary.UntilClamped {
		fmt.Fprintf(errOut, "gitcs: end date is in the future; using %s\n", now.Format(dateLayout))
	}

	var loading *spinner.Spinner
	if file, ok := errOut.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		loading = spinner.New(spinner.CharSets[6], 100*time.Millisecond,
			spinner.WithWriterFile(file), spinner.WithSuffix(" scanning repositories..."))
		loading.Start()
		defer loading.Stop()
	}
	scan, err := scanGitFolders(ctx, path)
	if err != nil {
		if loading != nil {
			loading.Stop()
		}
		return fail(err)
	}
	query := Query{Boundary: boundary, Emails: authors, BranchScope: BranchScope(scope), Now: now}
	report, err := processRepos(ctx, scan.Repos, query)
	if loading != nil {
		loading.Stop()
	}
	if err != nil {
		return fail(err)
	}
	report.Diagnostics = append(scan.Diagnostics, report.Diagnostics...)
	for _, diagnostic := range report.Diagnostics {
		fmt.Fprintf(errOut, "gitcs: skipped %q: %s\n", diagnostic.Path, diagnostic.Err)
	}
	if report.Processed == 0 && (len(scan.Repos) > 0 || len(report.Diagnostics) > 0) {
		return fail(fmt.Errorf("no repositories could be read; check the warnings above"))
	}
	if len(scan.Repos) == 0 {
		fmt.Fprintf(out, "No Git repositories found in %q. Try -path DIRECTORY to scan another folder.\n", path)
		return 0
	}
	if len(report.Diagnostics) > 0 {
		fmt.Fprintf(out, "Partial results: %d repositories processed; %d warning(s).\n", report.Processed, len(report.Diagnostics))
	}
	authorLabel := strings.Join(authors, ", ")
	if authorLabel == "*" {
		authorLabel = "all authors"
	}
	scopeLabel := "all local branches + HEAD"
	if query.BranchScope == HeadBranch {
		scopeLabel = "HEAD history"
	}
	fmt.Fprintf(out, "Authors: %s\nRepositories: %d/%d processed | Branches: %s\n", authorLabel, report.Processed, len(scan.Repos), scopeLabel)
	fmt.Fprintf(out, "Dates: author | Timezone: %s | Duplicate commits counted once\n", location)
	if len(report.Commits) == 0 {
		fmt.Fprintln(out, "No commits matched these authors and dates. Try -email '*' or adjust -since/-until.")
	}
	if width == 0 {
		width = 80
		if file, ok := out.(*os.File); ok {
			if columns, _, err := term.GetSize(int(file.Fd())); err == nil && columns >= 20 {
				width = columns
			}
		}
	}
	printTable(out, report.Days, boundary, width)
	return 0
}
