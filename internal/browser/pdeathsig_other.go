//go:build !linux && !windows

package browser

import "syscall"

// setPdeathsig is Linux-only. Elsewhere (macOS) Close and the process group clean up after a daemon
// that ends normally, and killStaleBrowser after one that crashed or was killed: at the next Launch
// it ends the browser that still holds the profile.
func setPdeathsig(*syscall.SysProcAttr) {}
