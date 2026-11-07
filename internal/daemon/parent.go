package daemon

import (
	"strings"
	"time"
)

// ConsoleHost is the image the Windows scheduled task runs tbd under (`conhost.exe --headless`).
const ConsoleHost = "conhost.exe"

// procInfo describes a process for the parent-watch decision.
type procInfo struct {
	PID     uint32
	Image   string // full path or base name
	Created time.Time
}

// parentWatchDecision decides whether `tbd run` should stop when its parent ends: only when the
// parent is the console host of the scheduled task. `schtasks /end` terminates that conhost.exe
// and nothing else (measured, D20), so tbd must follow it. A terminal, Explorer, WSL interop
// (wsl.exe) or a test harness as the parent watches nothing.
//
// The parent PID comes from a process snapshot, and the process now holding that PID might be a
// newer one that reused it after the real parent died: a process created after us cannot be our
// parent, so it is not watched.
func parentWatchDecision(self, parent procInfo) (bool, string) {
	switch {
	case parent.PID == 0:
		return false, "no parent"
	case !strings.EqualFold(baseName(parent.Image), ConsoleHost):
		return false, "parent is " + baseName(parent.Image)
	case parent.Created.IsZero() || self.Created.IsZero():
		return false, "creation time unknown"
	case parent.Created.After(self.Created):
		return false, "parent pid reused (that process is younger than tbd)"
	}
	return true, "parent is " + ConsoleHost
}

// ParentWatch follows the console host that started `tbd run` (Windows; see WatchConsoleHost).
type ParentWatch struct {
	PID   uint32
	Image string // base name
	done  chan struct{}
	close func()
}

// Done is closed when the watched parent has exited; nil (never ready) for a nil watch.
func (w *ParentWatch) Done() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.done
}

// Close stops watching.
func (w *ParentWatch) Close() {
	if w != nil && w.close != nil {
		w.close()
		w.close = nil
	}
}
