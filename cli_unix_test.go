//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunCancelsBlockedGitConfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("native Git is required for this integration test")
	}
	isolateGitConfig(t)
	fifo := filepath.Join(t.TempDir(), "blocked config")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", fifo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, errOut bytes.Buffer
	done := make(chan int, 1)
	root := t.TempDir()
	go func() {
		done <- run(ctx, []string{"-path", root}, &out, &errOut, testClock)
	}()
	// Opening a FIFO for writing succeeds only after Git has opened the read
	// end. Hold it open without supplying data to reproduce a blocked config.
	var writer *os.File
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		writer, err = os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0600)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if writer == nil {
		cancel()
		t.Fatal("Git did not open the configuration fixture")
	}
	defer writer.Close()
	cancel()
	select {
	case code := <-done:
		if code != 130 || out.Len() != 0 || !strings.Contains(errOut.String(), "context canceled") {
			t.Fatalf("blocked config cancellation: code=%d stdout=%s stderr=%s", code, &out, &errOut)
		}
	case <-time.After(3 * time.Second):
		// Release Git even if cancellation regresses, before touching the output
		// buffers or letting temporary-directory cleanup run.
		_ = writer.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		t.Fatal("cancellation did not stop a blocked Git configuration lookup")
	}
}

func TestRunUnreadableSubtreeWarnings(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires an unprivileged user")
	}
	root := t.TempDir()
	denied := filepath.Join(root, "denied")
	if err := os.Mkdir(denied, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(denied, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(denied, 0700) })
	warning := fmt.Sprintf("skipped %q:", filepath.Join(resolvedPath(t, root), "denied"))
	code, out, errOut := runForTest("-path", root, "-email", "*")
	if code != 1 || out != "" || !strings.Contains(errOut, warning) || !strings.Contains(errOut, "no repositories could be read") {
		t.Fatalf("unreadable tree appeared empty: code=%d\n%s\n%s", code, out, errOut)
	}
	createTestRepo(t, filepath.Join(root, "good"), "tester@test.com", 1, testClock)
	code, out, errOut = runForTest("-path", root, "-email", "*")
	if code != 0 || !strings.Contains(out, "Partial results: 1 repositories processed; 1 warning(s).") || !strings.Contains(out, "Total commits: 1 between") || !strings.Contains(errOut, warning) {
		t.Fatalf("unreadable subtree hid valid report: code=%d\n%s\n%s", code, out, errOut)
	}
}
