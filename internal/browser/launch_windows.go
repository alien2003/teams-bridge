//go:build windows

package browser

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// configureSysProcAttr is a no-op on Windows; the process tree is tracked with a job object.
func configureSysProcAttr(cmd *exec.Cmd) {}

// platformLaunchArgs adds nothing on Windows: msedge.exe runs with its sandbox and GPU as usual.
func platformLaunchArgs(headless bool) []string { return nil }

// procTree tracks msedge.exe and the children it spawns in a job object. The job has
// KILL_ON_JOB_CLOSE, so when the daemon exits for any reason (even a crash) Windows closes the
// handle and kills the browser tree: no orphan headless Edge keeps the profile locked. BREAKAWAY_OK
// lets Edge start a process that explicitly asks to leave the job (otherwise such a CreateProcess
// fails); those die with their browser anyway. If the job cannot be set up (for example inside a
// job that forbids it), taskkill /T is the fallback.
type procTree struct {
	cmd *exec.Cmd
	log *slog.Logger

	mu  sync.Mutex
	job windows.Handle // 0 when there is no job
}

func newProcTree(cmd *exec.Cmd, log *slog.Logger) *procTree {
	t := &procTree{cmd: cmd, log: log}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		log.Debug("browser: no job object; relying on taskkill", "err", err)
		return t
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		log.Debug("browser: job object limits failed; relying on taskkill", "err", err)
		return t
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		log.Debug("browser: cannot open the browser process for the job; relying on taskkill", "err", err)
		return t
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(job, ph); err != nil {
		_ = windows.CloseHandle(job)
		log.Debug("browser: cannot assign the browser to the job; relying on taskkill", "err", err)
		return t
	}
	t.job = job
	return t
}

func (t *procTree) handle() windows.Handle {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.job
}

// kill kills the whole tree: taskkill /T first (it walks the tree by parent PID while the browser is
// alive, so it also reaches children that left the job), then the job, then the process itself.
func (t *procTree) kill() {
	taskkill(t.cmd.Process.Pid)
	if job := t.handle(); job != 0 {
		_ = windows.TerminateJobObject(job, 1)
	}
	_ = t.cmd.Process.Kill()
}

// jobAccounting is JOBOBJECT_BASIC_ACCOUNTING_INFORMATION, which x/sys/windows does not define.
type jobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// sweep runs after msedge.exe exited: its children normally follow within moments (and must, or
// the profile stays locked); whatever is left in the job after a short wait is terminated.
func (t *procTree) sweep() {
	job := t.handle()
	if job == 0 {
		return
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var info jobAccounting
		if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil || info.ActiveProcesses == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = windows.TerminateJobObject(job, 1)
}

// release closes the job handle (which, with KILL_ON_JOB_CLOSE, ends anything still in it). It is
// called once the browser has exited, and is safe to call more than once.
func (t *procTree) release() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 {
		_ = windows.CloseHandle(t.job)
		t.job = 0
	}
}

// taskkill runs %SystemRoot%\System32\taskkill.exe /F /T /PID pid without a console window.
func taskkill(pid int) {
	exe := "taskkill.exe"
	if dir, err := windows.GetSystemDirectory(); err == nil {
		exe = filepath.Join(dir, "taskkill.exe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "/F", "/T", "/PID", strconv.Itoa(pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	_ = cmd.Run()
}
