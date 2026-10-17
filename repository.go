package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

// openRepository uses PlainOpen to resolve .git directories/files, then adds
// worktree common-directory support. go-git v5's EnableDotGitCommonDir leaves
// the commondir file open; ReadFile closes it before we construct the storage.
// This matters on Windows and for repeated scans in a long-lived explorer.
func openRepository(path string) (*git.Repository, error) {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return nil, err
	}
	storage := repo.Storer.(*filesystem.Storage)
	dot := storage.Filesystem()
	data, err := os.ReadFile(filepath.Join(dot.Root(), "commondir"))
	if os.IsNotExist(err) || err == nil && strings.TrimSpace(string(data)) == "" {
		return repo, nil
	}
	// The replacement storage owns a separate object cache; release the initial
	// storage both on errors and before handing the replacement to the caller.
	defer storage.Close()
	if err != nil {
		return nil, err
	}
	commonPath := strings.TrimSpace(string(data))
	if !filepath.IsAbs(commonPath) {
		commonPath = filepath.Join(dot.Root(), commonPath)
	}
	if _, err := os.Stat(commonPath); err != nil {
		if os.IsNotExist(err) {
			return nil, git.ErrRepositoryIncomplete
		}
		return nil, err
	}
	common := osfs.New(commonPath)
	combined := filesystem.NewStorage(dotgit.NewRepositoryFilesystem(dot, common), cache.NewObjectLRUDefault())
	repo, err = git.Open(combined, osfs.New(path))
	if err != nil {
		combined.Close()
		return nil, err
	}
	return repo, nil
}
