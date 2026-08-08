package main

import (
	"bytes"
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// processStartTime reports when the process now holding pid was started, which
// is what tells the wrapper that wrote a registry entry apart from an
// unrelated process that later inherited its pid.
func processStartTime(pid int) (time.Time, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil {
		return time.Time{}, false
	}
	tv := kp.Proc.P_starttime
	return time.Unix(tv.Sec, int64(tv.Usec)*1000), true
}

// processTTYDevice reports the device number of pid's controlling terminal.
// ok is false only when the kernel cannot be asked; a process that simply has
// no controlling terminal (NODEV) reports 0, true.
func processTTYDevice(pid int) (uint64, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil {
		return 0, false
	}
	tdev := kp.Eproc.Tdev
	if tdev == -1 { // NODEV
		return 0, true
	}
	// The same int32-to-uint64 conversion charDeviceNumber applies to
	// Stat_t.Rdev, so the two are directly comparable.
	return uint64(tdev), true //nolint:gosec // Tdev is a device number, not a signed quantity
}

// fGetPath is darwin's F_GETPATH: fill a MAXPATHLEN buffer with the path of
// the file behind a descriptor. It is absent from the syscall package.
const fGetPath = 50

// ttyPathByFD asks the kernel for the path of the open descriptor.
func ttyPathByFD(fd int) (string, error) {
	var buf [1024]byte
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), fGetPath, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return "", fmt.Errorf("tty path: fcntl F_GETPATH: %w", errno)
	}
	n := bytes.IndexByte(buf[:], 0)
	if n < 0 {
		n = len(buf)
	}
	path := string(buf[:n])
	if path == "" {
		return "", fmt.Errorf("tty path: fcntl F_GETPATH returned an empty path")
	}
	return path, nil
}
