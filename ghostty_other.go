//go:build !darwin

package main

import (
	"fmt"
	"runtime"
)

// Focusing a Ghostty surface is driven entirely through macOS accessibility
// (see ghostty_darwin.go), so everywhere else the navigator only exists to
// explain itself.
type ghosttyNavigator struct{}

func NewGhosttyNavigator() Navigator {
	return ghosttyNavigator{}
}

func (ghosttyNavigator) Focus(s LiveSession) error {
	return fmt.Errorf("cannot focus session %d: Ghostty navigation needs macOS accessibility, and this is %s",
		s.Entry.WrapperPID, runtime.GOOS)
}

func (ghosttyNavigator) Available() (bool, string) {
	return false, fmt.Sprintf("Ghostty navigation requires macOS (running on %s)", runtime.GOOS)
}
