//go:build !linux && !windows

package browser

import "syscall"

// setPdeathsig is Linux-only; elsewhere Close and the process group are the only cleanup.
func setPdeathsig(*syscall.SysProcAttr) {}
