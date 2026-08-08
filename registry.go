package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// registryDir returns the directory holding one JSON file per live osc8wrap
// process, creating it if needed.
func registryDir() (string, error) {
	base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "osc8wrap", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create registry directory: %w", err)
	}
	return dir, nil
}

// registerSession publishes e as <WrapperPID>.json and returns a cleanup that
// removes it. Registration is best effort: the returned cleanup is always safe
// to call, even when registration failed, so callers need no error branch.
func registerSession(e RegistryEntry) (func(), error) {
	noop := func() {}

	dir, err := registryDir()
	if err != nil {
		return noop, err
	}

	data, err := json.Marshal(e)
	if err != nil {
		return noop, fmt.Errorf("encode registry entry: %w", err)
	}

	path := filepath.Join(dir, fmt.Sprintf("%d.json", e.WrapperPID))
	if err := writeFileAtomic(dir, path, data); err != nil {
		return noop, err
	}

	var once sync.Once
	return func() {
		once.Do(func() { _ = os.Remove(path) })
	}, nil
}

// writeFileAtomic renames a temp file from the same directory into place so a
// concurrent loadRegistry never observes a half-written entry. The temp name
// deliberately does not end in .json so loadRegistry ignores it.
func writeFileAtomic(dir, path string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "entry-*.tmp")
	if err != nil {
		return fmt.Errorf("create registry temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write registry temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close registry temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("install registry entry: %w", err)
	}
	return nil
}

// loadRegistry returns the entries of processes that are still alive, sorted
// by wrapper pid. Entries of dead processes are pruned; unreadable or
// unparsable files are skipped rather than failing the whole listing, since a
// racing writer or a foreign file must not make `sessions` unusable.
func loadRegistry() ([]RegistryEntry, error) {
	dir, err := registryDir()
	if err != nil {
		return nil, err
	}

	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read registry directory: %w", err)
	}

	entries := make([]RegistryEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, de.Name())

		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var e RegistryEntry
		if err := json.Unmarshal(data, &e); err != nil {
			continue
		}
		if e.WrapperPID <= 0 {
			continue
		}
		if !entryOwnerAlive(e) {
			_ = os.Remove(path)
			continue
		}
		entries = append(entries, e)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].WrapperPID < entries[j].WrapperPID
	})
	return entries, nil
}

// startTimeSlack absorbs the gap between a process's real start time and the
// StartedAt the wrapper records a moment later, plus any clock granularity
// between the two sources.
const startTimeSlack = 2 * time.Second

// entryOwnerAlive reports whether the process that wrote e is still the one
// holding its pid. A live pid alone is not proof: an entry left behind by a
// killed wrapper (or by a reboot, which the registry outlives) would otherwise
// be resurrected the moment the pid is recycled, and focusing that phantom
// session writes an OSC 2 title into -- and navigates to -- whatever terminal
// now owns the recorded tty. StartedAt is recorded just after the wrapper
// started, so a process that started later cannot be that wrapper.
//
// The start time is best effort: where the platform cannot report one, the
// pid check stands alone as before.
func entryOwnerAlive(e RegistryEntry) bool {
	if !processAlive(e.WrapperPID) {
		return false
	}
	if e.StartedAt.IsZero() {
		return true
	}
	started, ok := processStartTime(e.WrapperPID)
	if !ok {
		return true
	}
	return !started.After(e.StartedAt.Add(startTimeSlack))
}

// ttyBelongsToOwner reports whether e.TTY is still plausibly the terminal of
// the process that recorded e. It is the second lock on the door entryOwnerAlive
// guards: writing a focus tag sends an OSC 2 title into the recorded device, so
// a mismatch here means the write would land in a terminal the user never asked
// about, and refusing beats corrupting it.
//
// Everything it cannot establish counts as a match, so a platform with no way
// to name a process's controlling terminal, or an entry that recorded none,
// behaves exactly as before. Only a positive contradiction -- the owner sits on
// a different device, or on none at all -- is a refusal.
func ttyBelongsToOwner(e RegistryEntry) bool {
	if e.TTY == "" || e.WrapperPID <= 0 {
		return true
	}
	want, ok := processTTYDevice(e.WrapperPID)
	if !ok {
		return true
	}
	if want == 0 {
		return false
	}
	fi, err := os.Stat(e.TTY)
	if err != nil {
		// The device is gone, so the write will fail on its own and say why.
		return true
	}
	got, ok := charDeviceNumber(fi)
	if !ok {
		return true
	}
	return got == want
}

// ttyPath returns the device path of the terminal behind f. Pass os.Stdout:
// that fd is the terminal emulator's own pty, so the path can be matched
// against what other tools report for the same surface.
func ttyPath(f *os.File) (string, error) {
	if f == nil {
		return "", fmt.Errorf("tty path: nil file")
	}
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("tty path: %s is not a terminal", f.Name())
	}

	path, err := ttyPathByFD(fd)
	if err == nil && isSameDevice(f, path) {
		return path, nil
	}

	// The fd-to-path shortcut is not available everywhere (and can report a
	// stale name), so fall back to matching the device number under /dev.
	path, scanErr := ttyPathByDevice(f)
	if scanErr == nil {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	return "", scanErr
}

// isSameDevice reports whether path names the same character device as f.
func isSameDevice(f *os.File, path string) bool {
	if path == "" {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	want, ok := charDeviceNumber(fi)
	if !ok {
		return false
	}
	other, err := os.Stat(path)
	if err != nil {
		return false
	}
	got, ok := charDeviceNumber(other)
	return ok && got == want
}

// ghosttySurfaceID reports the Ghostty surface hosting this process, or "" when
// not running under Ghostty.
func ghosttySurfaceID() string {
	return strings.TrimSpace(os.Getenv("GHOSTTY_SURFACE_ID"))
}
