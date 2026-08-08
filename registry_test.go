package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// useTempRegistry points registryDir at a scratch directory and returns it.
func useTempRegistry(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir, err := registryDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// deadPID returns the pid of a process that has already exited and been reaped.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestRegistryDir(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	dir, err := registryDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(state, "osc8wrap", "sessions")
	if dir != want {
		t.Errorf("got %q, want %q", dir, want)
	}

	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("got mode %o, want 700", perm)
	}
}

func TestRegistryDirFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)

	dir, err := registryDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "state", "osc8wrap", "sessions")
	if dir != want {
		t.Errorf("got %q, want %q", dir, want)
	}
}

func TestRegisterSessionRoundTrip(t *testing.T) {
	dir := useTempRegistry(t)

	entry := RegistryEntry{
		WrapperPID: os.Getpid(),
		ChildPID:   os.Getpid(),
		TTY:        "/dev/ttys011",
		SurfaceID:  "0x1552e82d3b500c1e",
		Cwd:        "/tmp/project",
		Command:    []string{"claude", "--resume"},
		StartedAt:  time.Now().Truncate(time.Second),
	}

	cleanup, err := registerSession(entry)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, strconv.Itoa(entry.WrapperPID)+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("registry file not written: %v", err)
	}

	entries, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	got := entries[0]
	if got.WrapperPID != entry.WrapperPID || got.ChildPID != entry.ChildPID ||
		got.TTY != entry.TTY || got.SurfaceID != entry.SurfaceID || got.Cwd != entry.Cwd {
		t.Errorf("got %+v, want %+v", got, entry)
	}
	if len(got.Command) != len(entry.Command) || got.Command[0] != entry.Command[0] {
		t.Errorf("got command %v, want %v", got.Command, entry.Command)
	}
	if !got.StartedAt.Equal(entry.StartedAt) {
		t.Errorf("got StartedAt %v, want %v", got.StartedAt, entry.StartedAt)
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("registry file still present after cleanup: %v", err)
	}
}

func TestRegisterSessionCleanupIsIdempotent(t *testing.T) {
	useTempRegistry(t)

	cleanup, err := registerSession(RegistryEntry{WrapperPID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	cleanup()

	entries, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries, want 0", len(entries))
	}
}

func TestRegisterSessionCleanupUsableAfterError(t *testing.T) {
	// A registry directory that cannot be created must not stop the wrapper.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", file)

	cleanup, err := registerSession(RegistryEntry{WrapperPID: os.Getpid()})
	if err == nil {
		t.Fatal("expected an error when the registry directory cannot be created")
	}
	if cleanup == nil {
		t.Fatal("cleanup must never be nil")
	}
	cleanup()
}

func TestRegisterSessionOverwritesStaleFileForSamePID(t *testing.T) {
	useTempRegistry(t)

	if _, err := registerSession(RegistryEntry{WrapperPID: os.Getpid(), TTY: "/dev/ttys001"}); err != nil {
		t.Fatal(err)
	}
	if _, err := registerSession(RegistryEntry{WrapperPID: os.Getpid(), TTY: "/dev/ttys002"}); err != nil {
		t.Fatal(err)
	}

	entries, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].TTY != "/dev/ttys002" {
		t.Errorf("got TTY %q, want /dev/ttys002", entries[0].TTY)
	}
}

func TestLoadRegistryPrunesDeadPIDs(t *testing.T) {
	dir := useTempRegistry(t)

	dead := deadPID(t)
	writeEntry(t, dir, RegistryEntry{WrapperPID: dead, TTY: "/dev/ttys900"})
	writeEntry(t, dir, RegistryEntry{WrapperPID: os.Getpid(), TTY: "/dev/ttys901"})

	entries, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].WrapperPID != os.Getpid() {
		t.Errorf("got pid %d, want %d", entries[0].WrapperPID, os.Getpid())
	}

	deadPath := filepath.Join(dir, strconv.Itoa(dead)+".json")
	if _, err := os.Stat(deadPath); !os.IsNotExist(err) {
		t.Errorf("dead entry was not pruned: %v", err)
	}
}

// A pid outliving the process that recorded it is the normal case after a
// SIGKILLed wrapper or a reboot: the entry must not be resurrected by whatever
// process inherits the pid, or focusing it writes into an unrelated terminal.
func TestLoadRegistryPrunesRecycledPIDs(t *testing.T) {
	if _, ok := processStartTime(os.Getpid()); !ok {
		t.Skip("process start times are unavailable on this platform")
	}
	dir := useTempRegistry(t)

	// This process is alive under the recorded pid but started long after the
	// entry claims to have been written, exactly as a recycled pid would.
	stale := RegistryEntry{
		WrapperPID: os.Getpid(),
		TTY:        "/dev/ttys900",
		StartedAt:  time.Now().Add(-24 * time.Hour),
	}
	writeEntry(t, dir, stale)

	entries, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("got %d entries, want 0: a recycled pid resurrected a dead session", len(entries))
	}
	path := filepath.Join(dir, strconv.Itoa(stale.WrapperPID)+".json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stale entry was not pruned: %v", err)
	}
}

// The mirror of the test above: a genuine entry, whose StartedAt follows the
// wrapper's own start, must survive.
func TestLoadRegistryKeepsLiveEntryWithStartTime(t *testing.T) {
	dir := useTempRegistry(t)

	writeEntry(t, dir, RegistryEntry{
		WrapperPID: os.Getpid(),
		TTY:        "/dev/ttys901",
		StartedAt:  time.Now(),
	})

	entries, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
}

// The pruning above is only as good as the clock behind it: a start time read
// in the wrong unit (linux reports clock ticks since boot, not seconds since
// the epoch) would be wrong by years and prune every live session. A process
// started just now must therefore look like it started just now.
func TestProcessStartTimeOfAFreshProcess(t *testing.T) {
	if _, ok := processStartTime(os.Getpid()); !ok {
		t.Skip("process start times are unavailable on this platform")
	}

	before := time.Now()
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	started, ok := processStartTime(cmd.Process.Pid)
	if !ok {
		t.Fatalf("no start time for the child we just started (pid %d)", cmd.Process.Pid)
	}
	after := time.Now()
	// A second of slack each way absorbs the granularity of the reported
	// start time; anything beyond that is a unit or epoch error.
	if started.Before(before.Add(-time.Second)) || started.After(after.Add(time.Second)) {
		t.Errorf("start time %v is outside the window [%v, %v] the child was started in", started, before, after)
	}
}

// startOnPTY runs a sleeping child with its own session and a pty for a
// controlling terminal, and returns its pid and the path of that terminal.
func startOnPTY(t *testing.T) (pid int, ttyDevice string) {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("cannot allocate a pty: %v", err)
	}
	t.Cleanup(func() {
		_ = ptmx.Close()
		_ = tty.Close()
	})

	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("cannot start a child on a pty: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	device, err := ttyPathByDevice(tty)
	if err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid, device
}

func TestTTYBelongsToOwner(t *testing.T) {
	pid, ownTTY := startOnPTY(t)
	if _, ok := processTTYDevice(pid); !ok {
		t.Skip("controlling terminals cannot be read on this platform")
	}
	// A second pty, sitting under no session at all: the stand-in for the
	// device a recycled pid never owned.
	_, strangerTTY := startOnPTY(t)

	// Nothing recorded, nothing to contradict: these must stay permissive, so
	// that no gap in what a platform can report blocks a legitimate focus.
	for _, e := range []RegistryEntry{
		{WrapperPID: pid, TTY: ownTTY},
		{WrapperPID: pid},
		{WrapperPID: pid, TTY: filepath.Join(t.TempDir(), "gone")},
		{WrapperPID: deadPID(t), TTY: strangerTTY},
	} {
		if !ttyBelongsToOwner(e) {
			t.Errorf("ttyBelongsToOwner(%+v) = false, want true (the terminal of pid %d is %s)", e, pid, ownTTY)
		}
	}

	// The destructive case: the entry names a device its recorded process is
	// demonstrably not on, so focusing it would retitle a stranger's terminal.
	stale := RegistryEntry{WrapperPID: pid, TTY: strangerTTY}
	if ttyBelongsToOwner(stale) {
		t.Errorf("ttyBelongsToOwner(%+v) = true, want false: %s is not the terminal of pid %d", stale, strangerTTY, pid)
	}
}

func TestLoadRegistrySkipsUnparsableFiles(t *testing.T) {
	dir := useTempRegistry(t)

	// A half-written file from a racing writer, plus a non-entry file.
	if err := os.WriteFile(filepath.Join(dir, "999999.json"), []byte(`{"wrapper_pid":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeEntry(t, dir, RegistryEntry{WrapperPID: os.Getpid()})

	entries, err := loadRegistry()
	if err != nil {
		t.Fatalf("malformed file broke the listing: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].WrapperPID != os.Getpid() {
		t.Errorf("got pid %d, want %d", entries[0].WrapperPID, os.Getpid())
	}
}

func TestLoadRegistrySortsByWrapperPID(t *testing.T) {
	dir := useTempRegistry(t)

	// Only live pids survive the load. pid 1 is alive but not ours, so it also
	// covers the EPERM case of the liveness check. The file names are ordered
	// independently of the pids so directory order cannot pass for sorting.
	pids := map[string]int{"a": os.Getpid(), "b": 1, "c": os.Getppid()}
	for name, pid := range pids {
		data, err := json.Marshal(RegistryEntry{WrapperPID: pid})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(pids) {
		t.Fatalf("got %d entries, want %d", len(entries), len(pids))
	}
	if entries[0].WrapperPID != 1 {
		t.Errorf("got first pid %d, want 1", entries[0].WrapperPID)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1].WrapperPID > entries[i].WrapperPID {
			t.Errorf("entries are not sorted by wrapper pid: %v", entries)
		}
	}
}

func TestLoadRegistryEmptyDir(t *testing.T) {
	useTempRegistry(t)

	entries, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries, want 0", len(entries))
	}
}

func TestTTYPathRejectsNonTTY(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close() //nolint:errcheck

	regular, err := os.CreateTemp(t.TempDir(), "regular-*")
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close() //nolint:errcheck

	tests := []struct {
		name string
		file *os.File
	}{
		{"dev null", devNull},
		{"regular file", regular},
		{"nil file", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, err := ttyPath(tt.file)
			if err == nil {
				t.Fatalf("got %q, want an error", path)
			}
			if path != "" {
				t.Errorf("got path %q alongside error, want empty", path)
			}
		})
	}
}

func TestTTYPathOnRealTerminal(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("cannot allocate a pty: %v", err)
	}
	defer ptmx.Close() //nolint:errcheck
	defer tty.Close()  //nolint:errcheck

	// The /dev scan is the fallback for platforms without an fd-to-path
	// shortcut, so check both routes agree on the same device node.
	byPath, err := ttyPath(tty)
	if err != nil {
		t.Fatal(err)
	}
	byDevice, err := ttyPathByDevice(tty)
	if err != nil {
		t.Fatal(err)
	}
	if byPath != byDevice {
		t.Errorf("ttyPath got %q, /dev scan got %q", byPath, byDevice)
	}

	if !filepath.IsAbs(byPath) {
		t.Errorf("got %q, want an absolute device path", byPath)
	}
	fi, err := os.Stat(byPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		t.Errorf("%s is not a character device", byPath)
	}
}

func TestGhosttySurfaceID(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{"unset", "", ""},
		{"plain", "0x1552e82d3b500c1e", "0x1552e82d3b500c1e"},
		{"trailing newline", "0xabc\n", "0xabc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GHOSTTY_SURFACE_ID", tt.env)
			if got := ghosttySurfaceID(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func writeEntry(t *testing.T, dir string, e RegistryEntry) {
	t.Helper()
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(e.WrapperPID)+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
