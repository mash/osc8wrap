package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// clockTicks is the USER_HZ that /proc reports times in. It is a fixed part of
// the /proc ABI on every architecture Go targets, deliberately independent of
// the kernel's internal CONFIG_HZ, so it can be a constant instead of a
// sysconf(_SC_CLK_TCK) call that would need cgo.
const clockTicks = 100

// procStatStarttimeIndex is the position of the starttime field once the
// fields up to and including comm have been cut off: starttime is field 22,
// and the first field after comm is state (field 3), so it is index 19.
const procStatStarttimeIndex = 19

// procStatTTYIndex is the position of tty_nr, field 7, counted the same way.
const procStatTTYIndex = 4

// processStartTime reports when the process now holding pid was started, which
// is what tells the wrapper that wrote a registry entry apart from an
// unrelated process that later inherited its pid. /proc reports the start as
// clock ticks since boot, so it is only as absolute as the boot time it is
// added to; both are best effort, and a failure leaves entryOwnerAlive with
// the pid check alone.
func processStartTime(pid int) (time.Time, bool) {
	fields, ok := procStatFields(pid)
	if !ok || len(fields) <= procStatStarttimeIndex {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseUint(fields[procStatStarttimeIndex], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	boot, ok := bootTime()
	if !ok {
		return time.Time{}, false
	}
	// The conversion is split so a long uptime cannot overflow the Duration:
	// ticks * time.Second wraps after a few years of uptime.
	//nolint:gosec // an uptime in ticks is far below the int64 range
	since := time.Duration(ticks/clockTicks)*time.Second +
		time.Duration(ticks%clockTicks)*(time.Second/clockTicks)
	return boot.Add(since), true
}

// processTTYDevice reports the device number of pid's controlling terminal.
// ok is false only when /proc cannot be read; a process that simply has no
// controlling terminal reports 0, true.
func processTTYDevice(pid int) (uint64, bool) {
	fields, ok := procStatFields(pid)
	if !ok || len(fields) <= procStatTTYIndex {
		return 0, false
	}
	// tty_nr is new_encode_dev()d exactly like the st_rdev the stat syscall
	// reports, so it compares directly against charDeviceNumber's result.
	dev, err := strconv.ParseInt(fields[procStatTTYIndex], 10, 64)
	if err != nil {
		return 0, false
	}
	return uint64(uint32(dev)), true //nolint:gosec // tty_nr is a device number, not a signed quantity
}

// procStatFields returns the fields of /proc/<pid>/stat that follow comm.
// comm is parenthesised and may itself contain spaces and ")", so the split
// starts after the last ")" rather than at the second field.
func procStatFields(pid int) ([]string, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nil, false
	}
	i := bytes.LastIndexByte(data, ')')
	if i < 0 {
		return nil, false
	}
	return strings.Fields(string(data[i+1:])), true
}

// bootTime reads the wall clock time the kernel booted at. It cannot change
// while the process runs, and /proc/stat is expensive to generate on a machine
// with many cores, so it is read once.
var bootTime = sync.OnceValues(func() (time.Time, bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close() //nolint:errcheck

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		rest, ok := strings.CutPrefix(scanner.Text(), "btime ")
		if !ok {
			continue
		}
		secs, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(secs, 0), true
	}
	return time.Time{}, false
})

// ttyPathByFD reads the /proc symlink pointing at the device behind the
// descriptor.
func ttyPathByFD(fd int) (string, error) {
	path, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil {
		return "", fmt.Errorf("tty path: readlink /proc/self/fd/%d: %w", fd, err)
	}
	return path, nil
}
