package main

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
)

type ScanResult struct {
	Repos       []string
	Diagnostics []Diagnostic
}

func scanGitFolders(ctx context.Context, root string) (ScanResult, error) {
	result := ScanResult{}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return result, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return result, err
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if cancelErr := ctx.Err(); cancelErr != nil {
			return cancelErr
		}
		if err != nil {
			if path == root {
				return err
			}
			result.Diagnostics = append(result.Diagnostics, Diagnostic{path, err})
			return nil
		}

		if d.Name() == ".git" {
			// Worktrees and submodules use a .git file pointing to their storage.
			result.Repos = append(result.Repos, filepath.Dir(path))
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip dependency directories
		_, ok := excludedFolders[strings.ToLower(d.Name())]
		if path != root && d.IsDir() && ok {
			return filepath.SkipDir
		}

		return nil
	})

	return result, err
}

// excludedFolders is a map of folder names (case-insensitive) to be excluded during the scan.
var excludedFolders = map[string]struct{}{
	"node_modules": {},
	"vendor":       {},
	".svn":         {},
	".hg":          {},
	".bzr":         {},
	"_vendor":      {},
	"godeps":       {},
	"bin":          {},
	"obj":          {},
	"tmp":          {},
	"build":        {},
	".vscode":      {},
	"dist":         {},
	"__pycache__":  {},
	".cache":       {},
	"coverage":     {},
	"target":       {},
	"out":          {},
	".idea":        {},
	".gradle":      {},
	".terraform":   {},
	"env":          {},
	".ds_store":    {},
	".next":        {},
	".nuxt":        {},
	".expo":        {},
	".circleci":    {},
	".github":      {},
	".gitlab":      {},
	".vagrant":     {},
	".serverless":  {},
}
