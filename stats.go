package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

type BranchScope string

const (
	LocalBranches BranchScope = "local"
	HeadBranch    BranchScope = "head"
)

type Query struct {
	Boundary    Boundary
	Emails      []string
	BranchScope BranchScope
	Now         time.Time
}

// CommitRecord is shared by the report and interactive explorer.
type CommitRecord struct {
	Hash          plumbing.Hash
	AuthorName    string
	AuthorEmail   string
	AuthorWhen    time.Time
	CommitterWhen time.Time
	Message       string
	Repositories  []string
}

type Diagnostic struct {
	Path string
	Err  error
}

type Report struct {
	Days        map[string]int
	Commits     []CommitRecord
	Processed   int
	Diagnostics []Diagnostic
}

func historyRoots(repo *git.Repository, scope BranchScope) ([]plumbing.Hash, error) {
	if scope != LocalBranches && scope != HeadBranch {
		return nil, fmt.Errorf("invalid branch scope %q; use local or head", scope)
	}
	var roots []plumbing.Hash
	head, err := repo.Head()
	if err == nil {
		roots = append(roots, head.Hash())
	} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, err
	}
	if scope == HeadBranch {
		return roots, nil
	}
	refs, err := repo.References()
	if err != nil {
		return nil, err
	}
	defer refs.Close()
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if !ref.Name().IsBranch() {
			return nil
		}
		resolved, err := repo.Reference(ref.Name(), true)
		if err != nil {
			return err
		}
		roots = append(roots, resolved.Hash())
		return nil
	})
	return roots, err
}

func readRepo(ctx context.Context, path string, q Query) ([]CommitRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repo, err := git.PlainOpenWithOptions(path, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return nil, err
	}
	roots, err := historyRoots(repo, q.BranchScope)
	if err != nil {
		return nil, err
	}
	shallow, err := repo.Storer.Shallow()
	if err != nil {
		return nil, err
	}
	shallowSet := make(map[plumbing.Hash]bool, len(shallow))
	for _, hash := range shallow {
		shallowSet[hash] = true
	}
	seen := make(map[plumbing.Hash]bool)
	var commits []CommitRecord
	since, until := q.Boundary.Since.Format(dateLayout), q.Boundary.Until.Format(dateLayout)
	for len(roots) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hash := roots[len(roots)-1]
		roots = roots[:len(roots)-1]
		if seen[hash] {
			continue
		}
		seen[hash] = true
		commit, err := repo.CommitObject(hash)
		if err != nil {
			return nil, err
		}
		if !shallowSet[hash] {
			roots = append(roots, commit.ParentHashes...)
		}
		// Author dates need not be monotonic after a rebase or merge.
		// Filter and group by the same clock without pruning older ancestors.
		when := commit.Author.When
		date := dateKey(when, q.Boundary.Location)
		if date < since || date >= until || when.After(q.Now) {
			continue
		}
		if !slices.Contains(q.Emails, "*") && !slices.Contains(q.Emails, commit.Author.Email) {
			continue
		}
		commits = append(commits, CommitRecord{
			Hash: hash, AuthorName: commit.Author.Name, AuthorEmail: commit.Author.Email,
			AuthorWhen: when, CommitterWhen: commit.Committer.When, Message: commit.Message,
			Repositories: []string{path},
		})
	}
	return commits, nil
}

type repositoryResult struct {
	path    string
	commits []CommitRecord
	err     error
}

// readRepositories streams completed repositories. Cancellation also releases
// workers when an interactive consumer quits before draining the channel.
func readRepositories(ctx context.Context, repos []string, q Query) <-chan repositoryResult {
	jobs := make(chan string)
	results := make(chan repositoryResult)
	var workers sync.WaitGroup
	for range min(8, len(repos)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for path := range jobs {
				commits, err := readRepo(ctx, path, q)
				select {
				case results <- repositoryResult{path, commits, err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, path := range repos {
			select {
			case jobs <- path:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()
	return results
}

type reportBuilder struct {
	report Report
	byHash map[plumbing.Hash]int
	query  Query
}

func newReportBuilder(q Query) *reportBuilder {
	return &reportBuilder{
		report: Report{Days: make(map[string]int)},
		byHash: make(map[plumbing.Hash]int), query: q,
	}
}

func (b *reportBuilder) add(result repositoryResult) {
	if result.err != nil {
		b.report.Diagnostics = append(b.report.Diagnostics, Diagnostic{result.path, result.err})
		return
	}
	b.report.Processed++
	for _, commit := range result.commits {
		if i, exists := b.byHash[commit.Hash]; exists {
			paths := b.report.Commits[i].Repositories
			if !slices.Contains(paths, result.path) {
				b.report.Commits[i].Repositories = append(paths, result.path)
			}
			continue
		}
		b.byHash[commit.Hash] = len(b.report.Commits)
		b.report.Commits = append(b.report.Commits, commit)
		b.report.Days[dateKey(commit.AuthorWhen, b.query.Boundary.Location)]++
	}
}

func (b *reportBuilder) finish() Report {
	report := b.report
	for i := range report.Commits {
		sort.Strings(report.Commits[i].Repositories)
	}
	sort.Slice(report.Commits, func(i, j int) bool {
		a, b := report.Commits[i], report.Commits[j]
		if !a.AuthorWhen.Equal(b.AuthorWhen) {
			return a.AuthorWhen.After(b.AuthorWhen)
		}
		return a.Hash.String() < b.Hash.String()
	})
	sort.Slice(report.Diagnostics, func(i, j int) bool { return report.Diagnostics[i].Path < report.Diagnostics[j].Path })
	return report
}

func processRepos(ctx context.Context, repos []string, q Query) (Report, error) {
	builder := newReportBuilder(q)
	for result := range readRepositories(ctx, repos, q) {
		builder.add(result)
	}
	return builder.finish(), ctx.Err()
}
