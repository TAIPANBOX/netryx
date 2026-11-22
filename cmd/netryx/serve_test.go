package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain lets a test run the real binary's main: the test binary started
// again with NETRYX_TEST_MAIN=1 is `netryx` with the arguments it was given.
func TestMain(m *testing.M) {
	if os.Getenv("NETRYX_TEST_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A reload of the service is a SIGHUP (the init's reload_service, sent by
// procd whenever /etc/config/netryx or the network changes). serve must read
// its rules again and go on with the same process: without a handler the
// default action of SIGHUP ends it.
func TestServeReloadsOnAHangUpAndKeepsRunning(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("serve runs on Linux")
	}
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	cmd := exec.Command(os.Args[0], "serve", "-capture=false", "-dns=false", "-fetch-icons=false",
		"-addr=127.0.0.1:0", "-sock="+filepath.Join(dir, "ctl.sock"), "-state="+filepath.Join(dir, "state"), "-geo="+missing, "-apps="+missing,
		"-oui="+missing, "-meta="+missing, "-leases="+missing, "-zone=Europe/London")
	cmd.Env = append(os.Environ(), "NETRYX_TEST_MAIN=1")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	lines := make(chan string, 256)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	wait := func(what string) {
		t.Helper()
		deadline := time.After(20 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("serve ended while waiting for %q", what)
				}
				if strings.Contains(l, what) {
					return
				}
			case <-deadline:
				t.Fatalf("serve never logged %q", what)
			}
		}
	}
	wait("netryx on http://")
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	wait("SIGHUP")
	time.Sleep(300 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("serve did not survive the hang-up: %v", err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for range lines {
		}
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve must stop cleanly on SIGTERM after a reload: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not stop on SIGTERM")
	}
}
