//go:build windows

package daemon

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// requestStopOS sets the holder's stop event (listenStop); "" when nobody listens on it.
func requestStopOS(home string, _ int) (string, error) {
	ok, err := signalStopEvent(home)
	if err != nil || !ok {
		return "", err
	}
	return "stop event", nil
}

// forceStop terminates pid, but only if its image is tbd.exe and it still
// holds the lock. The checks and TerminateProcess use one handle: while it is open the PID cannot
// be reused, so the process that was checked is the one that is ended. Exit code 0: the stop was
// requested, and Task Scheduler must not treat it as a failure to restart.
func forceStop(home string, pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	img, err := processImage(h)
	if err != nil {
		return fmt.Errorf("image of process %d: %w", pid, err)
	}
	if !isTbdImage(img) {
		return fmt.Errorf("process %d is %s, not tbd.exe: not ending it", pid, baseName(img))
	}
	if err := verifyHolder(home, pid); err != nil {
		return err
	}
	if err := windows.TerminateProcess(h, 0); err != nil {
		return fmt.Errorf("terminate process %d: %w", pid, err)
	}
	_, _ = windows.WaitForSingleObject(h, uint32((10 * time.Second).Milliseconds()))
	return nil
}

// processImage is the full path of the process's image.
func processImage(h windows.Handle) (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}

// processCreated is the creation time of the process.
func processCreated(h windows.Handle) (time.Time, error) {
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(h, &c, &e, &k, &u); err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, c.Nanoseconds()), nil
}
