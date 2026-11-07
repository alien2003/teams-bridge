//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

func lockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }
func unlockFile(f *os.File)     { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// listenStop has nothing to do on Unix: `tbd stop` sends SIGTERM, which `tbd run` and `tbd login`
// already turn into a graceful shutdown (signalContext).
func listenStop(*os.File) (*stopListener, error) { return nil, nil }
