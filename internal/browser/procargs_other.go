//go:build !linux && !darwin && !windows

package browser

import "errors"

// procArgs is not implemented on this OS, so killStaleBrowser never ends a process here.
func procArgs(int) ([]string, error) {
	return nil, errors.New("reading another process's arguments is not implemented on this OS")
}
