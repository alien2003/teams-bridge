//go:build linux

package browser

import "syscall"

// setPdeathsig makes the kernel SIGKILL the browser when the process that started it dies.
func setPdeathsig(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGKILL }
