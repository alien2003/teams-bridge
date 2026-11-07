package browser

import (
	"os"
	"strconv"
	"strings"
)

// procArgs reads the argv of pid from /proc/<pid>/cmdline.
func procArgs(pid int) ([]string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00"), nil
}
