//go:build windows

package daemon

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The stop event is named after the lock file's identity, not after the path's spelling.
func TestStopEventNameFollowsTheFile(t *testing.T) {
	home := t.TempDir()
	l, err := AcquireLock(home)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if l.StopErr != nil || l.StopRequested() == nil {
		t.Fatalf("no stop listener: %v", l.StopErr)
	}
	name, err := stopEventNameOf(l.f)
	if err != nil {
		t.Fatal(err)
	}
	other, err := os.Open(strings.ToUpper(LockPath(home)))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if n2, err := stopEventNameOf(other); err != nil || n2 != name {
		t.Errorf("upper-case spelling: %q %v, want %q", n2, err, name)
	}
	l2, err := AcquireLock(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Release()
	if n3, _ := stopEventNameOf(l2.f); n3 == name {
		t.Error("two data directories share a stop event")
	}
	// Setting it reaches the holder.
	if ok, err := signalStopEvent(strings.ToUpper(home)); !ok || err != nil {
		t.Fatalf("signalStopEvent: %v %v", ok, err)
	}
	select {
	case <-l.StopRequested():
	default:
		<-l.StopRequested() // closes asynchronously; a hang here fails the test by timeout
	}
	select {
	case <-l2.StopRequested():
		t.Error("the other holder was asked to stop too")
	default:
	}
}

// TestStopLegacyTbdExe is the first `tbd stop` on a PC that still runs a tbd.exe from before D20:
// no stop event, so Stop waits out its timeout and then ends the process, which is checked to be
// tbd.exe (a copy of this test binary under that name) and to hold the lock.
func TestStopLegacyTbdExe(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "tbd.exe")
	copyFile(t, self, exe)
	home := t.TempDir()
	_, exited, exitErr := helperAt(t, exe, home, "legacy")
	if ok, err := signalStopEvent(home); ok || err != nil {
		t.Fatalf("the legacy helper has a stop event: %v %v", ok, err)
	}
	var out bytes.Buffer
	if err := Stop(context.Background(), StopOptions{Home: home, Out: &out, Timeout: time.Second}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, want := range []string{"does not listen", "ending it forcibly", "Ended."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	select {
	case <-exited:
		if err := exitErr(); err != nil {
			t.Errorf("helper exit (TerminateProcess with 0): %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("helper still running")
	}
	if held, pid := Held(home); held {
		t.Errorf("lock still held (pid %s)", pid)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}
