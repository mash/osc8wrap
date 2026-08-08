package main

import (
	"strings"
	"testing"
)

// The navigator must satisfy the shared interface on every platform, even
// where it is only a stub.
var _ Navigator = NewGhosttyNavigator()

func TestGhosttyNavigator_AvailableOutsideGhostty(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")

	ok, reason := NewGhosttyNavigator().Available()
	if ok {
		t.Fatal("navigation reported available while not running under Ghostty")
	}
	if strings.TrimSpace(reason) == "" {
		t.Error("unavailable navigator gave no reason")
	}
}

func TestGhosttyNavigator_FocusRejectsUnknownSurface(t *testing.T) {
	// A session with no surface id cannot be located on any platform: on
	// darwin there is nothing to walk to, elsewhere there is no walker.
	s := LiveSession{Entry: RegistryEntry{WrapperPID: 4242, TTY: "/dev/ttys999"}}

	err := NewGhosttyNavigator().Focus(s)
	if err == nil {
		t.Fatal("Focus succeeded for a session with no surface id")
	}
	if !strings.Contains(err.Error(), "4242") {
		t.Errorf("error does not name the session: %v", err)
	}
}
