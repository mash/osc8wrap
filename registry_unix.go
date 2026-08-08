//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ttyDeviceGlobs are the places a pty slave can appear: BSD-style numbered
// slaves on darwin, the devpts mount on linux.
var ttyDeviceGlobs = []string{"/dev/ttys*", "/dev/pts/*", "/dev/tty*"}

// processAlive reports whether pid can still be signalled. EPERM means the
// process exists but belongs to another user, which still counts as alive;
// only ESRCH proves it is gone. Anything else is treated as alive so a
// transient error never prunes a live entry.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return !errors.Is(err, syscall.ESRCH)
}

// charDeviceNumber returns the device number of a character device.
func charDeviceNumber(fi os.FileInfo) (uint64, bool) {
	if fi.Mode()&os.ModeCharDevice == 0 {
		return 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Rdev), true //nolint:gosec // Rdev is a device number, not a signed quantity
}

// ttyPathByDevice finds the device node for f by scanning /dev for a character
// device with the same device number.
func ttyPathByDevice(f *os.File) (string, error) {
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("tty path: stat fd: %w", err)
	}
	want, ok := charDeviceNumber(fi)
	if !ok {
		return "", fmt.Errorf("tty path: fd is not a character device")
	}

	seen := make(map[string]bool)
	for _, glob := range ttyDeviceGlobs {
		matches, err := filepath.Glob(glob)
		if err != nil {
			continue
		}
		for _, candidate := range matches {
			if seen[candidate] {
				continue
			}
			seen[candidate] = true

			cfi, err := os.Stat(candidate)
			if err != nil {
				continue
			}
			if got, ok := charDeviceNumber(cfi); ok && got == want {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("tty path: no device under /dev matches fd")
}
