package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestScanExclusionsAreCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	for name := range excludedFolders {
		if err := os.MkdirAll(filepath.Join(root, strings.ToUpper(name), "dependency", ".git"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Similar names remain valid, and finding a repository does not hide a
	// nested submodule or independent repository below its working tree.
	for _, path := range []string{"vendor-tools/.git", "project/.git", "project/submodule/.git"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0755); err != nil {
			t.Fatal(err)
		}
	}
	result, err := scanGitFolders(context.Background(), root)
	expectedRoot := resolvedPath(t, root)
	want := []string{filepath.Join(expectedRoot, "project"), filepath.Join(expectedRoot, "project", "submodule"), filepath.Join(expectedRoot, "vendor-tools")}
	if err != nil || len(result.Diagnostics) != 0 || !slices.Equal(result.Repos, want) {
		t.Fatalf("exclusions hid a real repository or scanned dependencies: %+v, %v; want %v", result, err, want)
	}
}

func TestScanDoesNotFollowNestedSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "real", ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"alias": filepath.Join(root, "real"), "loop": root, "outside": outside, "broken": filepath.Join(root, "missing")} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	result, err := scanGitFolders(context.Background(), root)
	if err != nil || len(result.Diagnostics) != 0 || !slices.Equal(result.Repos, []string{filepath.Join(resolvedPath(t, root), "real")}) {
		t.Fatalf("nested symlinks were traversed: %+v, %v", result, err)
	}
}
