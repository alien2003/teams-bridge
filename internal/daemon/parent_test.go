package daemon

import (
	"testing"
	"time"
)

func TestParentWatchDecision(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	self := procInfo{PID: 200, Image: `C:\tbd\tbd.exe`, Created: t0}
	for _, c := range []struct {
		name   string
		parent procInfo
		want   bool
	}{
		{"the scheduled task's conhost", procInfo{PID: 100, Image: `C:\Windows\System32\conhost.exe`, Created: t0.Add(-time.Millisecond)}, true},
		{"conhost, upper case, same tick", procInfo{PID: 100, Image: `C:\WINDOWS\system32\CONHOST.EXE`, Created: t0}, true},
		{"PID reused by a younger conhost", procInfo{PID: 100, Image: `C:\Windows\System32\conhost.exe`, Created: t0.Add(time.Second)}, false},
		{"terminal shell", procInfo{PID: 100, Image: `C:\Windows\System32\cmd.exe`, Created: t0.Add(-time.Hour)}, false},
		{"PowerShell", procInfo{PID: 100, Image: `C:\Program Files\PowerShell\7\pwsh.exe`, Created: t0.Add(-time.Hour)}, false},
		{"Explorer", procInfo{PID: 100, Image: `C:\Windows\explorer.exe`, Created: t0.Add(-time.Hour)}, false},
		{"WSL interop", procInfo{PID: 100, Image: `C:\Program Files\WSL\wsl.exe`, Created: t0.Add(-time.Hour)}, false},
		{"test harness", procInfo{PID: 100, Image: `C:\sb\test\cmd\tbd\tbd.test.exe`, Created: t0.Add(-time.Hour)}, false},
		{"no parent", procInfo{}, false},
		{"creation time unknown", procInfo{PID: 100, Image: `conhost.exe`}, false},
	} {
		if got, why := parentWatchDecision(self, c.parent); got != c.want || why == "" {
			t.Errorf("%s: watch=%v (%s), want %v", c.name, got, why, c.want)
		}
	}
	var w *ParentWatch
	if w.Done() != nil {
		t.Error("nil watch has a Done channel")
	}
	w.Close() // nil-safe
}
