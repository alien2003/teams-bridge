//go:build !windows && !linux && !darwin

package daemon

import "os"

// checkLockOwner: no owner check on other Unix systems; the settled PID file is the evidence.
func checkLockOwner(*os.File, int) error { return nil }
