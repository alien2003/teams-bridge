package browser

import "golang.org/x/sys/unix"

// procArgs reads the argv of pid with the kern.procargs2 sysctl (the source of ps; it works for
// the user's own processes).
func procArgs(pid int) ([]string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	return parseProcArgs2(b)
}
