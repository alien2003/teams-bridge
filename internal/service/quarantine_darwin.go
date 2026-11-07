package service

import "golang.org/x/sys/unix"

// quarantined reports whether path carries the com.apple.quarantine attribute that browsers and
// most download tools set (curl and `gh release download` do not). Gatekeeper then checks the
// binary on its first start, and an unsigned, unnotarized one is refused until the user clears
// the attribute or allows it in System Settings.
func quarantined(path string) bool {
	_, err := unix.Getxattr(path, "com.apple.quarantine", nil)
	return err == nil
}
