//go:build !windows

package daemon

// WatchConsoleHost watches nothing outside Windows: systemd stops the daemon with SIGTERM.
func WatchConsoleHost() (*ParentWatch, string) { return nil, "not Windows" }
