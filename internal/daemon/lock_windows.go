//go:build windows

package daemon

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// Windows byte-range locks are mandatory: locking byte 0 would also stop other processes (tbd
// status) from reading the PID written there. Lock one byte far beyond the end of the file instead.
func lockRange() *windows.Overlapped { return &windows.Overlapped{OffsetHigh: 0x7fffffff} }

func lockFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lockRange())
}

func unlockFile(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockRange())
}

// kernelLockOwners: Windows has no API that names a byte-range lock's owner.
func kernelLockOwners(*os.File) ([]int, bool) { return nil, false }

// stopEventNameOf is the name of the stop event for the lock file f: StopEventName of its volume
// serial number and file index, which are the same whichever path spelling (case, 8.3 short name,
// junction, subst drive) opened the file.
func stopEventNameOf(f *os.File) (string, error) {
	var fi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &fi); err != nil {
		return "", err
	}
	return StopEventName(fi.VolumeSerialNumber, uint64(fi.FileIndexHigh)<<32|uint64(fi.FileIndexLow)), nil
}

// listenStop creates the lock holder's stop event (manual reset, not signalled) and watches it.
//
// Why a named event: the daemon under the scheduled task runs in a hidden console
// (conhost --headless) with no window, so there is no console to send Ctrl+Break to
// (GenerateConsoleCtrlEvent reaches only processes on the caller's own console) and no window to
// post WM_CLOSE to, and Windows has no SIGTERM. A kernel event in the session's Local\ namespace is
// reachable from any process of the same user in the same logon session (the task runs with
// InteractiveToken, i.e. in the user's desktop session), needs no port or pipe protocol, and its
// default DACL admits only the creating user and SYSTEM. Its name derives from the lock file, so a
// stop reaches exactly the process that holds this data directory, whatever its PID is.
func listenStop(f *os.File) (*stopListener, error) {
	name, err := stopEventNameOf(f)
	if err != nil {
		return nil, err
	}
	n16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	ev, err := windows.CreateEvent(nil, 1, 0, n16)
	if ev == 0 {
		return nil, err
	}
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		// A stopper of the previous holder may still keep the old, signalled event open.
		_ = windows.ResetEvent(ev)
	}
	quit, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(ev)
		return nil, err
	}
	l := &stopListener{done: make(chan struct{})}
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		i, err := windows.WaitForMultipleObjects([]windows.Handle{ev, quit}, false, windows.INFINITE)
		if err == nil && i == windows.WAIT_OBJECT_0 {
			close(l.done)
		}
	}()
	l.close = func() {
		_ = windows.SetEvent(quit)
		<-exited
		_ = windows.CloseHandle(ev)
		_ = windows.CloseHandle(quit)
	}
	return l, nil
}

// signalStopEvent sets the stop event of the lock file in home. ok is false when no process has
// that event open (a holder from before `tbd stop` existed, or one in another logon session).
func signalStopEvent(home string) (ok bool, err error) {
	f, err := os.OpenFile(LockPath(home), os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	name, err := stopEventNameOf(f)
	if err != nil {
		return false, err
	}
	n16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false, err
	}
	ev, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, n16)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(ev)
	return true, windows.SetEvent(ev)
}
