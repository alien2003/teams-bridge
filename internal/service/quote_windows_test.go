package service

import (
	"syscall"
	"testing"
	"unsafe"
)

// windowsArgv splits cmdline with the real CommandLineToArgvW.
func windowsArgv(t *testing.T, cmdline string) []string {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(cmdline)
	if err != nil {
		t.Fatal(err)
	}
	var argc int32
	argv, err := syscall.CommandLineToArgv(p, &argc)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.LocalFree(syscall.Handle(uintptr(unsafe.Pointer(argv))))
	out := make([]string, argc)
	for i := range out {
		out[i] = syscall.UTF16ToString((*argv[i])[:])
	}
	return out
}

// TestWinQuoteAgainstCommandLineToArgvW checks winQuote and the test's reference parser against
// the parser Windows itself uses.
func TestWinQuoteAgainstCommandLineToArgvW(t *testing.T) {
	for _, s := range winQuoteCases {
		for _, force := range []bool{false, true} {
			line := `conhost.exe --a ` + winQuote(s, force) + ` --b`
			got := windowsArgv(t, line)
			if len(got) != 4 || got[2] != s {
				t.Errorf("CommandLineToArgvW(%s) = %q, want %q at [2]", line, got, s)
			}
			if ref := parseWinArgs(line[len("conhost.exe "):]); len(ref) != 3 || ref[1] != s {
				t.Errorf("reference parser disagrees on %s: %q", line, ref)
			}
		}
	}
	args := TaskArguments(Spec{Exe: `C:\Program Files\tbd\tbd.exe`, Home: `C:\Users\a b\tbd\`, ExtraArgs: []string{`x"y`}})
	got := windowsArgv(t, `conhost.exe `+args)
	want := []string{"conhost.exe", "--headless", `C:\Program Files\tbd\tbd.exe`, "run", "--home", `C:\Users\a b\tbd\`, `x"y`}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
