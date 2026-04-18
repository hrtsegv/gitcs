package main

import (
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const sixMonthsInDays int = 182

var now = time.Now()

func fillCommits(path, email string, commits map[int]int, b Boundary, mu *sync.Mutex) error {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return err
	}

	commitIterator, err := repo.Log(&git.LogOptions{Since: &b.Since, Until: &b.Until})
	if err != nil {
		return err
	}

	err = commitIterator.ForEach(func(c *object.Commit) error {
		if email != "*" && c.Author.Email != email {
			return nil
		}

		days := daysAgo(c.Author.When)
		if days < 0 {
			return nil
		}

		mu.Lock()
		commits[days]++
		mu.Unlock()
		return nil
	})
	return err
}

func processRepos(repos []string, email string, b Boundary) map[int]int {
	m := map[int]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, repo := range repos {
		wg.Add(1)
		go func(r string) {
			defer wg.Done()
			err := fillCommits(r, email, m, b, &mu)
			if err != nil {
				// We don't want to spam stdout during spinner
			}
		}(repo)
	}
	wg.Wait()
	return m
}
