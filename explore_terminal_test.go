//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// Run the real UI in an isolated process attached to a pseudo terminal. Exiting
// here also checks that runExplorer restores the terminal before returning.
func TestExplorerPTYHelper(t *testing.T) {
	if os.Getenv("GITCS_TEST_PTY") != "1" {
		return
	}
	before, err := term.GetState(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "terminal baseline:", err)
		os.Exit(99)
	}
	code := runWithInput(context.Background(), []string{
		"explore", "-path", os.Getenv("GITCS_TEST_ROOT"), "-email", "*", "-timezone", "UTC",
	}, os.Stdin, os.Stdout, os.Stderr, testClock)
	// Check before the session leader exits: Darwin invalidates terminal ioctls
	// on the parent's slave descriptor after that session has ended.
	after, err := term.GetState(int(os.Stdin.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		fmt.Fprintln(os.Stderr, "UI did not restore terminal attributes:", err)
		os.Exit(99)
	}
	os.Exit(code)
}

func TestExplorerTerminalLifecycle(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		name := "quit"
		if interrupted {
			name = "interrupt"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			repo := createTestRepo(t, root, "tester@test.com", 0, testClock)
			addTestCommit(t, repo, "PTY smoke commit", "tester@test.com", testClock, testClock)
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			if err := pty.Setsize(master, &pty.Winsize{Cols: 80, Rows: 24}); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestExplorerPTYHelper$", "-test.timeout=30s")
			command.Env = append(os.Environ(), "GITCS_TEST_PTY=1", "GITCS_TEST_ROOT="+root, "TERM=xterm-256color", "NO_COLOR=1")
			command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
			command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer command.Process.Kill()
			chunks := make(chan string, 64)
			readCtx, stopRead := context.WithCancel(context.Background())
			defer stopRead()
			go func() {
				defer close(chunks)
				buf := make([]byte, 4096)
				for {
					n, err := master.Read(buf)
					if n > 0 {
						select {
						case chunks <- string(buf[:n]):
						case <-readCtx.Done():
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
			var output bytes.Buffer
			waitFor := func(value string) {
				t.Helper()
				timer := time.NewTimer(15 * time.Second)
				defer timer.Stop()
				for !strings.Contains(output.String(), value) {
					select {
					case chunk, ok := <-chunks:
						if !ok {
							t.Fatalf("terminal closed before %q: %q", value, output.String())
						}
						output.WriteString(chunk)
					case <-timer.C:
						t.Fatalf("timed out waiting for %q: %q", value, output.String())
					}
				}
			}
			waitFor("PTY smoke commit")
			if !strings.Contains(output.String(), "\x1b[?1049h") {
				t.Fatal("UI did not enter the alternate screen")
			}
			// Search must consume q as text; Escape restores the previous filter.
			output.Reset()
			if _, err := io.WriteString(master, "/q"); err != nil {
				t.Fatal(err)
			}
			// The renderer can draw "Search: " and "q" separately. Wait for
			// the newly rendered empty-result row instead of a joined raw prompt.
			waitFor("No matches.")
			output.Reset()
			if _, err := io.WriteString(master, "\x1b"); err != nil {
				t.Fatal(err)
			}
			// Wait for Escape to be decoded before sending another key. Otherwise
			// a legacy terminal can interpret Escape + '?' as Alt+?.
			waitFor("PTY smoke commit")
			output.Reset()
			if err := pty.Setsize(master, &pty.Winsize{Cols: 120, Rows: 35}); err != nil {
				t.Fatal(err)
			}
			if err := command.Process.Signal(syscall.SIGWINCH); err != nil {
				t.Fatal(err)
			}
			// Opening help proves that q in the search didn't exit the program.
			if _, err := io.WriteString(master, "?"); err != nil {
				t.Fatal(err)
			}
			waitFor("Explore commits")
			quit := "q"
			if interrupted {
				quit = "\x03"
			}
			if _, err := io.WriteString(master, quit); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			select {
			case err := <-done:
				if interrupted {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 130 {
						t.Fatalf("interrupt exit: %v", err)
					}
				} else if err != nil {
					t.Fatalf("quit exit: %v", err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("terminal UI did not quit")
			}
			waitFor("\x1b[?1049l")
		})
	}
}
