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

// CommitRecord retains the information needed for a future commit explorer.
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

func processRepos(ctx context.Context, repos []string, q Query) (Report, error) {
	report := Report{Days: make(map[string]int)}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	type result struct {
		path    string
		commits []CommitRecord
		err     error
	}
	jobs := make(chan string)
	results := make(chan result)
	var workers sync.WaitGroup
	// Bound concurrent history reads even for very large workspaces.
	for range min(8, len(repos)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for path := range jobs {
				commits, err := readRepo(ctx, path, q)
				results <- result{path, commits, err}
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
	byHash := make(map[plumbing.Hash]int)
	for result := range results {
		if result.err != nil {
			report.Diagnostics = append(report.Diagnostics, Diagnostic{result.path, result.err})
			continue
		}
		report.Processed++
		for _, commit := range result.commits {
			if i, exists := byHash[commit.Hash]; exists {
				paths := report.Commits[i].Repositories
				if !slices.Contains(paths, result.path) {
					report.Commits[i].Repositories = append(paths, result.path)
				}
				continue
			}
			byHash[commit.Hash] = len(report.Commits)
			report.Commits = append(report.Commits, commit)
			report.Days[dateKey(commit.AuthorWhen, q.Boundary.Location)]++
		}
	}
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
	return report, ctx.Err()
}
