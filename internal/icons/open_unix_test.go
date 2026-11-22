//go:build unix

package icons

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOpeningAnIconNeverFollowsALinkOrWaitsOnAPipe(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.png")
	if err := os.WriteFile(target, pngBody, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.png")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if fh, err := openIcon(link); err == nil {
		fh.Close()
		t.Error("openIcon followed a symlink")
	}

	// A FIFO swapped in under an icon's name after the Lstat would block a
	// plain open until a writer came; the handler must not hang on it.
	fifo := filepath.Join(dir, "fifo.png")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no FIFOs here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		fh, err := openIcon(fifo)
		if err == nil {
			fh.Close()
		}
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("openIcon is still waiting on a FIFO")
	}

	if fh, err := openIcon(target); err != nil {
		t.Errorf("openIcon refused a regular file: %v", err)
	} else {
		fh.Close()
	}
}
