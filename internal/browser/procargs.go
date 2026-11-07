package browser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
)

// lockPID parses the target of Chromium's SingletonLock symlink, "<hostname>-<pid>" (the host
// name may itself contain dashes).
func lockPID(target string) (int, bool) {
	i := strings.LastIndexByte(target, '-')
	if i <= 0 || i == len(target)-1 {
		return 0, false
	}
	pid, err := strconv.Atoi(target[i+1:])
	if err != nil || pid <= 1 || strconv.Itoa(pid) != target[i+1:] {
		return 0, false
	}
	return pid, true
}

// parseProcArgs2 decodes the buffer of macOS's kern.procargs2 sysctl: argc (a 32-bit integer in
// host order, little-endian on every Mac), the executable path, NUL padding, then argc
// NUL-terminated arguments (the environment follows, which is not read).
func parseProcArgs2(b []byte) ([]string, error) {
	if len(b) < 4 {
		return nil, errors.New("procargs2: short buffer")
	}
	argc := int(int32(binary.LittleEndian.Uint32(b)))
	if argc < 0 || argc > 1<<16 {
		return nil, errors.New("procargs2: bad argc")
	}
	rest := b[4:]
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return nil, errors.New("procargs2: no executable path")
	}
	rest = rest[i:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	args := make([]string, 0, argc)
	for len(args) < argc {
		j := bytes.IndexByte(rest, 0)
		if j < 0 {
			if len(rest) == 0 {
				break
			}
			j = len(rest)
		}
		args = append(args, string(rest[:j]))
		if j == len(rest) {
			rest = nil
		} else {
			rest = rest[j+1:]
		}
	}
	if len(args) != argc {
		return nil, errors.New("procargs2: truncated arguments")
	}
	return args, nil
}
