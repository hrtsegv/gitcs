package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestScanGitFolders(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"one/.git", "nested/two/.git", "node_modules/ignored/.git", "one/.git/fake/.git"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0755); err != nil {
			t.Fatal(err)
		}
	}
	linked := filepath.Join(root, "linked")
	if err := os.Mkdir(linked, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: ../one/.git"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := scanGitFolders(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	// Temp directory parents can themselves be symlinks (e.g. /var on macOS).
	// The scanner returns absolute paths with those parent symlinks resolved.
	expectedRoot := resolvedPath(t, root)
	want := []string{filepath.Join(expectedRoot, "one"), filepath.Join(expectedRoot, "nested", "two"), filepath.Join(expectedRoot, "linked")}
	if len(result.Repos) != len(want) || len(result.Diagnostics) != 0 {
		t.Fatalf("unexpected scan: %+v", result)
	}
	for _, path := range want {
		if !slices.Contains(result.Repos, path) {
			t.Fatalf("missing %s in %v", path, result.Repos)
		}
	}
}

func TestExplicitExcludedRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "build")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	result, err := scanGitFolders(context.Background(), root)
	if err != nil || len(result.Repos) != 1 {
		t.Fatalf("explicit root was ignored: %+v, %v", result, err)
	}
}

func TestScanEmptyMissingAndCanceled(t *testing.T) {
	root := t.TempDir()
	result, err := scanGitFolders(context.Background(), root)
	if err != nil || len(result.Repos) != 0 {
		t.Fatalf("empty scan: %+v, %v", result, err)
	}
	if _, err := scanGitFolders(context.Background(), filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing root should fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanGitFolders(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestScanPermissionWarning(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix directory permissions and an unprivileged user")
	}
	root := t.TempDir()
	denied := filepath.Join(root, "denied")
	if err := os.Mkdir(denied, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "good", ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(denied, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(denied, 0700) })
	expectedDenied := filepath.Join(resolvedPath(t, root), "denied")
	result, err := scanGitFolders(context.Background(), root)
	if err != nil || len(result.Repos) != 1 || len(result.Diagnostics) != 1 || result.Diagnostics[0].Path != expectedDenied {
		t.Fatalf("unreadable subtree hid valid results: %+v, %v", result, err)
	}
	if _, err := scanGitFolders(context.Background(), denied); err == nil {
		t.Fatal("unreadable root should fail")
	}
}

func TestScanSymlinkRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	result, err := scanGitFolders(context.Background(), link)
	if err != nil || len(result.Repos) != 1 || result.Repos[0] != resolvedPath(t, root) {
		t.Fatalf("symlink root: %+v, %v", result, err)
	}
}

func TestScanSymlinkedParent(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(filepath.Join(root, "project", ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "parent-alias")
	if err := os.Symlink(parent, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// The root is a directory beneath a symlink, rather than a symlink itself.
	result, err := scanGitFolders(context.Background(), filepath.Join(alias, "workspace"))
	expected := filepath.Join(resolvedPath(t, root), "project")
	if err != nil || len(result.Repos) != 1 || result.Repos[0] != expected || len(result.Diagnostics) != 0 {
		t.Fatalf("symlinked parent: %+v, %v; want repository %s", result, err, expected)
	}
}
