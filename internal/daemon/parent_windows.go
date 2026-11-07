//go:build windows

package daemon

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WatchConsoleHost opens the parent process early (SYNCHRONIZE: from then on its PID cannot be
// reused under us) and, when parentWatchDecision says so, watches it: Done closes once it exits.
// Without a watch it returns nil and the reason.
func WatchConsoleHost() (*ParentWatch, string) {
	ppid, err := parentPID()
	if err != nil {
		return nil, "parent unknown: " + err.Error()
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, ppid)
	if err != nil {
		return nil, "parent not accessible (exited?)"
	}
	keep := false
	defer func() {
		if !keep {
			_ = windows.CloseHandle(h)
		}
	}()
	img, _ := processImage(h)
	pc, _ := processCreated(h)
	sc, _ := processCreated(windows.CurrentProcess())
	if ok, why := parentWatchDecision(procInfo{PID: uint32(os.Getpid()), Created: sc}, procInfo{PID: ppid, Image: img, Created: pc}); !ok {
		return nil, why
	}
	quit, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, "no event: " + err.Error()
	}
	keep = true
	w := &ParentWatch{PID: ppid, Image: baseName(img), done: make(chan struct{})}
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		i, err := windows.WaitForMultipleObjects([]windows.Handle{h, quit}, false, windows.INFINITE)
		if err == nil && i == windows.WAIT_OBJECT_0 {
			close(w.done)
		}
	}()
	w.close = func() {
		_ = windows.SetEvent(quit)
		<-exited
		_ = windows.CloseHandle(h)
		_ = windows.CloseHandle(quit)
	}
	return w, "parent is " + ConsoleHost
}

// parentPID is this process's parent PID from a process snapshot.
func parentPID() (uint32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snap)
	self := uint32(os.Getpid())
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == self {
			return e.ParentProcessID, nil
		}
	}
	return 0, errors.New("this process is not in the snapshot")
}
