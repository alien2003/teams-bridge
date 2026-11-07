//go:build !darwin

package service

// quarantined: only macOS has com.apple.quarantine.
func quarantined(string) bool { return false }
