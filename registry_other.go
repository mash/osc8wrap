//go:build !darwin && !linux

package main

import (
	"fmt"
	"time"
)

// processStartTime is unavailable here, so entryOwnerAlive falls back to the
// pid check alone.
func processStartTime(pid int) (time.Time, bool) {
	return time.Time{}, false
}

// processTTYDevice cannot be answered here either, so ttyBelongsToOwner has
// nothing to check and lets the focus attempt through.
func processTTYDevice(pid int) (uint64, bool) {
	return 0, false
}

// ttyPathByFD has no shortcut outside darwin and linux; ttyPath falls back to
// scanning /dev for a matching device.
func ttyPathByFD(fd int) (string, error) {
	return "", fmt.Errorf("tty path: no fd-to-path lookup on this platform")
}
