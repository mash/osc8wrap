package main

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// Ghostty ships no url scheme, no AppleScript dictionary and no CLI IPC for
// focusing a surface, and its XTWINOPS support stops at size queries, so
// "CSI 5 t" cannot raise a window either. macOS accessibility is the only
// route. So we tag the target surface by setting its window title, because the
// title tracks the focused surface.
//
// Finding the tag then happens in two phases.
//
// A tab's radio button carries the *title of that tab's focused surface* even
// while the tab is in the background, so the whole tab bar of every window can
// be read without selecting anything. That is the locate phase: one read-only
// pass, no press, no raise, no activate, and it names the window and tab of the
// target before a single thing on screen moves. Nothing else identifies a
// surface -- a Ghostty window's AXIdentifier is the constant string
// "TerminalWindowRestoration" and neither windows nor tab radio buttons expose
// a surface id -- so the OSC 2 title tag remains the only handle.
//
// Locating and then acting on what was located is one osascript invocation, not
// two. Each spawn costs ~67 ms of process startup plus a fresh accessibility
// handshake before a single attribute is read, and the two phases have nothing
// to say to Go in between: the script that found the tab is the script that
// should press it. A tag that no tab bar carries is reported back as "miss",
// which is the only answer that returns to Go before anything has moved.
//
// A background *split* has no radio button of its own, so its title is invisible
// to the locate phase. That is the walk phase, and it is the original algorithm:
// select each tab in turn and cycle its splits, comparing the window title after
// every step. It is a linear search whose every probe is a visible tab switch,
// which is exactly why the locate phase exists to skip it whenever it can.

const (
	// ghosttyBinary is where the app bundle keeps its CLI. Ghostty puts
	// nothing on PATH, so the absolute path is the only reliable probe.
	ghosttyBinary = "/Applications/Ghostty.app/Contents/MacOS/ghostty"

	// defaultSplitKeybind is Ghostty's stock chord for goto_split:next.
	defaultSplitKeybind = "super+]"

	// focusFoundPrefix is what buildFocusScript's script prints once the
	// window title matches the tag and that window is confirmed to be in
	// front. The trailing space keeps it from being a prefix of "notfound".
	focusFoundPrefix = "found "

	// focusNotFrontPrefix is what the script prints when it did locate the
	// tagged surface but could not leave its window in front. That is a
	// failure: the user would be looking at some other window.
	focusNotFrontPrefix = "notfront "

	// locateMissResult is what the locate/select script prints when no tab bar
	// in any window carries the tag. The target is then a background split,
	// whose title no radio button exposes, and only the walk can reach it.
	locateMissResult = "miss"

	// locatePolls is how many times the tag is written and the tab bar read
	// before giving up on the locate phase. See the comment on the loop in
	// locateAndSelect: this is a race against the target repainting its own
	// title, and one attempt loses it on any session that is doing work. Only
	// this outer loop can re-tag, because only Go can write to the pty.
	locatePolls = 3
)

// osaRunner executes an AppleScript and returns its result. Tests substitute a
// fake so that no test can move the user's window focus.
type osaRunner interface {
	run(script string) (string, error)
}

type execOsaRunner struct{}

func (execOsaRunner) run(script string) (string, error) {
	cmd := exec.Command("osascript", "-e", script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

type ghosttyNavigator struct {
	runner osaRunner
	// splitKeybind is a func rather than a string so tests never shell out to
	// the real Ghostty binary to discover it.
	splitKeybind func() string
	// writeTitle is injectable so a test can observe that the tag is re-written
	// before every locate poll; a plain file cannot show that, since each write
	// starts at offset 0 and overwrites the last one.
	writeTitle func(tty, title string) error
}

func NewGhosttyNavigator() Navigator {
	return &ghosttyNavigator{
		runner:       execOsaRunner{},
		splitKeybind: splitNextKeybind,
		writeTitle:   writeTerminalTitle,
	}
}

func (n *ghosttyNavigator) Available() (bool, string) {
	if term := os.Getenv("TERM_PROGRAM"); term != "ghostty" {
		if term == "" {
			return false, "not running under Ghostty (TERM_PROGRAM is unset)"
		}
		return false, fmt.Sprintf("not running under Ghostty (TERM_PROGRAM is %q)", term)
	}
	if _, err := os.Stat(ghosttyBinary); err != nil {
		return false, fmt.Sprintf("Ghostty not found at %s", ghosttyBinary)
	}
	return true, ""
}

func (n *ghosttyNavigator) Focus(s LiveSession) error {
	if s.Entry.SurfaceID == "" {
		return fmt.Errorf("session %d was not started under Ghostty (no GHOSTTY_SURFACE_ID recorded)", s.Entry.WrapperPID)
	}
	if s.Entry.TTY == "" {
		return fmt.Errorf("session %d recorded no controlling terminal, so its window cannot be tagged", s.Entry.WrapperPID)
	}

	writeTitle := n.writeTitle
	if writeTitle == nil {
		writeTitle = writeTerminalTitle
	}

	tag := focusTag(s.Entry.WrapperPID)
	if err := writeTitle(s.Entry.TTY, tag); err != nil {
		return fmt.Errorf("tagging %s: %w", s.Entry.TTY, err)
	}
	// The tag is scaffolding; never leave it on screen, even if the walk below
	// fails or the AppleScript errors out.
	defer func() { _ = writeTitle(s.Entry.TTY, restoredTitle(s)) }()

	if handled, err := n.locateAndSelect(s, tag, writeTitle); handled {
		return err
	}

	keystroke, err := keybindToAppleScript(n.splitKeybind())
	if err != nil {
		return fmt.Errorf("cannot drive Ghostty's split navigation: %w", err)
	}

	out, err := n.runner.run(buildFocusScript(tag, keystroke))
	if err != nil {
		return fmt.Errorf("osascript failed while walking Ghostty windows: %w", err)
	}
	if handled, err := n.interpretFocusResult(s, out); handled {
		return err
	}
	return fmt.Errorf("surface %s of session %d (%s) was not found: every Ghostty window, tab and split was visited without the window title matching the tag",
		s.Entry.SurfaceID, s.Entry.WrapperPID, s.Entry.TTY)
}

// locateAndSelect reads every window's tab bar looking for the tag and, on a
// hit, raises that window, presses that one tab and confirms the result -- all
// inside a single osascript. It reports handled=false when the caller must fall
// back to the visible walk: either the tag was never seen in a tab bar (the
// target is a background split), or the script itself failed, which is not
// worth aborting a focus over.
func (n *ghosttyNavigator) locateAndSelect(s LiveSession, tag string, writeTitle func(tty, title string) error) (bool, error) {
	for poll := range locatePolls {
		if poll > 0 {
			// Re-tag before every retry. claude repaints its own terminal title
			// constantly -- the braille spinner is a title update -- so on a
			// busy session the tag can be overwritten between the write and the
			// read, turning every hit into a miss and silently costing the whole
			// optimisation. One attempt is not enough; this loop is the only
			// thing standing between "jumps straight there" and "steps through
			// every tab" for a session that is doing work. It looks redundant on
			// an idle session, which is exactly why it must not be simplified
			// away.
			if err := writeTitle(s.Entry.TTY, tag); err != nil {
				return false, nil
			}
		}
		out, err := n.runner.run(buildLocateSelectScript(tag))
		if err != nil {
			return false, nil
		}
		// Anything the script did not recognise -- "miss" above all -- means
		// the tab bar did not answer this time, so re-tag and read again.
		if handled, err := n.interpretFocusResult(s, out); handled {
			return handled, err
		}
	}
	return false, nil
}

// interpretFocusResult turns a script's "found"/"notfront" report into Focus's
// return value. It reports handled=false for anything else, which for the walk
// means "notfound" and for the select phase should not happen at all.
func (n *ghosttyNavigator) interpretFocusResult(s LiveSession, out string) (bool, error) {
	switch {
	case strings.HasPrefix(out, focusFoundPrefix):
		return true, nil
	case strings.HasPrefix(out, focusNotFrontPrefix):
		return true, fmt.Errorf("surface %s of session %d (%s) was found at %s but Ghostty would not bring that window to the front",
			s.Entry.SurfaceID, s.Entry.WrapperPID, s.Entry.TTY, strings.TrimPrefix(out, focusNotFrontPrefix))
	default:
		return false, nil
	}
}

// focusTag builds a window title that no real program would produce, so a
// stale tag can never be mistaken for a match. The wrapper pid makes it
// readable if it ever does linger on screen.
func focusTag(wrapperPID int) string {
	return fmt.Sprintf("osc8wrap⟦%d-%s⟧", wrapperPID, rand.Text()[:10])
}

// restoredTitle is the title to put back after tagging. claude repaints its
// own title within a second, but a wrapped command that never sets a title
// would otherwise keep the tag forever.
func restoredTitle(s LiveSession) string {
	if s.Name != "" {
		return s.Name
	}
	if len(s.Entry.Command) > 0 {
		return filepath.Base(s.Entry.Command[0])
	}
	return "osc8wrap"
}

// writeTerminalTitle sets the terminal emulator's window and tab title by
// writing OSC 2 to the pty the emulator owns. O_NOCTTY keeps this process from
// adopting the device as its controlling terminal.
func writeTerminalTitle(tty, title string) error {
	f, err := os.OpenFile(tty, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck
	// A control character in the title would terminate the OSC early and leak
	// the rest into the terminal as commands.
	title = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, title)
	_, err = fmt.Fprintf(f, "\033]2;%s\033\\", title)
	return err
}

// pollHandlers is the one place that waits for the window server to catch up.
// Every fixed "delay" this file used to carry was waiting for a specific,
// observable condition -- the app became frontmost, the raised window reached
// the front, the pressed tab's title arrived -- so each is now a bounded poll on
// that condition instead. The common case returns as soon as the UI has caught
// up rather than always paying the worst case.
//
// checkNow is the predicate, selected by name because AppleScript handlers
// cannot take a block. Every read is inside a try: "name of window 1" answers
// "missing value" while a window is being torn down, and a process with no
// windows raises outright.
//
// The tick budgets are chosen so the worst case is no slower than the fixed
// delays they replace. One tick is a 0.02 s sleep plus one accessibility read
// (~0.008 s here), so 5 ticks stands in for a "delay 0.15" and 7 for a
// "delay 0.2".
const pollHandlers = `on checkNow(what, target)
	tell application "System Events"
		tell process "Ghostty"
			if what is "frontmost" then
				try
					return frontmost is true
				end try
				return false
			end if
			try
				if what is "title" then return (name of window 1) is target
				if what is "position" then return (position of window 1) is target
			end try
			return false
		end tell
	end tell
end checkNow

on pollUntil(what, target, maxTicks)
	repeat maxTicks times
		if my checkNow(what, target) then return true
		delay 0.02
	end repeat
	return my checkNow(what, target)
end pollUntil`

// confirmHandlers is shared by the walk and by the locate/select script,
// because both owe the same proof. Matching the tag is not by itself evidence
// that anything moved: the raise is wrapped in a try, and the accessibility
// window order can be stale. So confirmFront raises the window once more and
// only then checks that the frontmost window of a frontmost, AXMain Ghostty is
// the tagged one, and finish() reports "notfront" when it is not, so a search
// that located the tag but moved nothing can never be read as success.
//
// The AXMain value is compared rather than concatenated: an AX boolean thrown
// straight into a string concatenation raises error -1700.
const confirmHandlers = `on confirmFront(theTag)
	tell application "System Events"
		tell process "Ghostty"
			try
				perform action "AXRaise" of window 1
			end try
			my pollUntil("title", theTag, 5)
			if (name of window 1) is not theTag then return false
			if frontmost is false then return false
			set isMain to true
			try
				set isMain to (value of attribute "AXMain" of window 1) is true
			end try
			return isMain
		end tell
	end tell
end confirmFront

on finish(theTag, whereFound)
	if confirmFront(theTag) then return "found " & whereFound
	return "notfront " & whereFound
end finish`

// restoreFrontHandler puts the user back where they were when the walk failed
// to find anything. Without it a search that visits every tab of every window
// abandons the user on whatever tab it happened to stop at, which is a worse
// place than where they started.
//
// The window is re-found by its screen position rather than by an index or a
// reference: AXRaise re-sorts the window list as the walk goes, so the starting
// window's index is long gone by the time this runs, a saved reference resolves
// by name and every tab click renames its window (error -1728), and a Ghostty
// window's AXIdentifier is the constant "TerminalWindowRestoration". Raising a
// window does not move it, so its position survives the walk intact.
const restoreFrontHandler = `on restoreFront(startPos, startTab)
	if startPos is missing value then return
	tell application "System Events"
		tell process "Ghostty"
			repeat with ri from 1 to (count of windows)
				set samePos to false
				try
					set samePos to ((position of window ri) is startPos)
				end try
				if samePos then
					try
						perform action "AXRaise" of window ri
					end try
					my pollUntil("position", startPos, 5)
					if startTab > 0 then
						try
							perform action "AXPress" of radio button startTab of tab group 1 of window 1
						end try
					end if
					return
				end if
			end repeat
		end tell
	end tell
end restoreFront`

// locateSelectScriptTemplate reads the title of every tab of every window and,
// if the tag is there, jumps straight at that window and that tab: one raise,
// one AXPress, and the same confirmation the walk uses. No intermediate tab is
// ever selected. If the tag is nowhere in a tab bar it returns "miss", having
// pressed, raised and activated nothing -- that is the whole point, since a
// locate that perturbs focus is just the walk with extra steps. It works
// because a tab's radio button carries the title of that tab's focused surface
// even while the tab is in the background.
//
// The titles of one window's tabs are read with a single "title of radio
// buttons of tab group 1", not one read per radio button. Each element read is
// its own accessibility round trip -- eight of them measured 204 ms against
// 102 ms for the bulk read -- and the bulk form makes the scan's cost
// independent of how many tabs are open. The returned list is positional and
// 1-indexed, which is exactly the index AXPress needs.
//
// The scan is retried a few times rather than preceded by a fixed delay: the
// OSC 2 the caller just wrote needs a moment to reach Ghostty and land in the
// accessibility tree, and a scan that finds it immediately should not have
// waited. Only Go can re-write the tag, so a scan that keeps missing returns
// "miss" and lets the caller re-tag; see locatePolls.
//
// A window whose single tab means Ghostty draws no tab bar has no radio buttons
// at all, so it reports zero tabs and is left to the walk -- which finds it on
// its first step anyway, with no visible stepping.
//
// The window is addressed by its pre-raise index exactly twice, to read its
// position and to raise it; AXRaise then re-sorts the process's window list, so
// everything after addresses the raised window as window 1. That saved position
// is what the poll after the raise waits on -- a position, never a window
// reference, which would resolve by name and go stale (error -1728).
//
// The tab is pressed rather than focused: setting AXFocused reports success and
// moves nothing. Indices are coerced with "as text" before concatenation, and
// every tab title is read inside a try, since a radio button can answer
// "missing value".
const locateSelectScriptTemplate = pollHandlers + `

` + confirmHandlers + `

set theTag to "%[1]s"
set foundWindow to 0
set foundTab to 0
set targetPos to missing value
tell application "System Events"
	tell process "Ghostty"
		repeat 3 times
			set windowTotal to (count of windows)
			repeat with wi from 1 to windowTotal
				set tabNames to {}
				try
					set tabNames to title of radio buttons of tab group 1 of window wi
				end try
				repeat with ti from 1 to (count of tabNames)
					set tabName to ""
					try
						set tabName to (item ti of tabNames) as text
					end try
					if tabName is theTag then
						set foundWindow to wi
						set foundTab to ti
						exit repeat
					end if
				end repeat
				if foundWindow > 0 then exit repeat
			end repeat
			if foundWindow > 0 then exit repeat
			delay 0.02
		end repeat
		if foundWindow > 0 then
			try
				set targetPos to position of window foundWindow
			end try
		end if
	end tell
end tell
if foundWindow is 0 then return "` + locateMissResult + `"
tell application "Ghostty" to activate
pollUntil("frontmost", true, 7)
tell application "System Events"
	tell process "Ghostty"
		set frontmost to true
		try
			perform action "AXRaise" of window foundWindow
		end try
		if targetPos is not missing value then my pollUntil("position", targetPos, 5)
		try
			perform action "AXPress" of radio button foundTab of tab group 1 of window 1
		end try
		my pollUntil("title", theTag, 5)
	end tell
end tell
return finish(theTag, "window " & (foundWindow as text) & " tab " & (foundTab as text))`

// buildLocateSelectScript is pure so the script can be diffed in tests instead
// of being observed against the user's live Ghostty.
func buildLocateSelectScript(tag string) string {
	return fmt.Sprintf(locateSelectScriptTemplate, appleScriptString(tag))
}

// focusScriptTemplate walks every Ghostty window, selecting each tab and then
// cycling that tab's splits, until the window title matches the tag.
//
// This is the fallback, reached only when the locate phase missed, which means
// the target is a background split. Every probe here is a visible tab switch,
// so it must not run when the tab bar already answered the question. It ends by
// restoring the tab it started from if it found nothing at all -- see
// restoreFrontHandler.
//
// Every window is raised before it is inspected, for two reasons. Reporting
// "found" for a window that was never raised would leave the user looking at
// whatever was in front (activating the app does not reorder its windows), and
// the split keystrokes only reach the frontmost window anyway. The already
// selected tab of a background window carries the tag from the moment Focus
// writes it, so a match that is checked before the raise is exactly the case
// that would silently do nothing.
//
// A match is therefore not reported directly: it goes through finish(), which
// raises the window once more and only then confirms that the frontmost window
// of a frontmost Ghostty is the tagged one. Anything else is reported as
// "notfront" so a walk that moved nothing can never be read as success.
//
// Three AppleScript traps are baked into its shape. Saving a window reference
// ("set w to window 1") captures it by name, and selecting a tab renames the
// window, which invalidates the saved reference with error -1728 -- so every
// access re-resolves the window inline. AXRaise re-sorts the process's
// front-to-back window list, so the index that was just raised no longer names
// that window: the raised one is window 1 and the rest of the iteration
// addresses it that way, while the next iteration's "window wi" is still an
// unvisited window because the already-visited ones have been pushed down into
// positions 2..wi. And a split's AXScrollArea sits at a different depth with
// and without splits, so the surfaces are counted through "entire contents"
// rather than at a fixed depth. Setting AXFocused on that scroll area reports
// success and moves nothing, which is why splits are cycled with the keybind
// instead.
const focusScriptTemplate = `on walkSplits(theTag)
	tell application "System Events"
		tell process "Ghostty"
			set areaTotal to 0
			repeat with el in (entire contents of window 1)
				try
					if class of el is scroll area then set areaTotal to areaTotal + 1
				end try
			end repeat
			repeat (areaTotal - 1) times
				%[2]s
				my pollUntil("title", theTag, 5)
				if (name of window 1) is theTag then return true
			end repeat
		end tell
	end tell
	return false
end walkSplits

` + pollHandlers + `

` + confirmHandlers + `

` + restoreFrontHandler + `

set theTag to "%[1]s"
tell application "Ghostty" to activate
pollUntil("frontmost", true, 7)
tell application "System Events"
	tell process "Ghostty"
		set frontmost to true
		set startPos to missing value
		try
			set startPos to position of window 1
		end try
		set startTab to 0
		set startTabTotal to 0
		try
			set startTabTotal to (count of radio buttons of tab group 1 of window 1)
		end try
		repeat with si from 1 to startTabTotal
			try
				if (value of radio button si of tab group 1 of window 1) is 1 then set startTab to si
			end try
		end repeat
		set windowTotal to (count of windows)
		repeat with wi from 1 to windowTotal
			try
				perform action "AXRaise" of window wi
			end try
			my pollUntil("title", theTag, 5)
			if (name of window 1) is theTag then return my finish(theTag, "window " & wi)
			set tabTotal to 0
			try
				set tabTotal to (count of radio buttons of tab group 1 of window 1)
			end try
			if tabTotal is 0 then
				if my walkSplits(theTag) then return my finish(theTag, "window " & wi)
			else
				repeat with ti from 1 to tabTotal
					try
						click radio button ti of tab group 1 of window 1
					end try
					my pollUntil("title", theTag, 5)
					if (name of window 1) is theTag then return my finish(theTag, "window " & wi & " tab " & ti)
					if my walkSplits(theTag) then return my finish(theTag, "window " & wi & " tab " & ti)
				end repeat
			end if
		end repeat
	end tell
end tell
restoreFront(startPos, startTab)
return "notfound"`

// buildFocusScript is pure so the script can be diffed in tests instead of
// being observed through its effect on the user's windows.
func buildFocusScript(tag string, keystroke string) string {
	return fmt.Sprintf(focusScriptTemplate, appleScriptString(tag), keystroke)
}

// appleScriptString escapes a value for interpolation into an AppleScript
// string literal.
func appleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

var (
	splitKeybindOnce  sync.Once
	splitKeybindCache string
)

// splitNextKeybind reports the chord bound to goto_split:next, asking Ghostty
// itself so a user's rebinding is honoured. Ghostty is not going to be
// reconfigured mid-run, so the answer is cached.
func splitNextKeybind() string {
	splitKeybindOnce.Do(func() {
		out, err := exec.Command(ghosttyBinary, "+list-keybinds").Output()
		if err != nil {
			splitKeybindCache = defaultSplitKeybind
			return
		}
		splitKeybindCache = parseSplitNextKeybind(string(out))
	})
	return splitKeybindCache
}

// parseSplitNextKeybind reads `ghostty +list-keybinds` output, whose lines
// look like "keybind = super+]=goto_split:next".
func parseSplitNextKeybind(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "keybind"); ok {
			rest = strings.TrimSpace(rest)
			if !strings.HasPrefix(rest, "=") {
				continue
			}
			line = strings.TrimSpace(rest[1:])
		}
		// The action never contains "=" but a chord can, so split at the last one.
		i := strings.LastIndex(line, "=")
		if i < 0 || strings.TrimSpace(line[i+1:]) != "goto_split:next" {
			continue
		}
		if chord := strings.TrimSpace(line[:i]); chord != "" {
			return chord
		}
	}
	return defaultSplitKeybind
}

// appleScriptModifiers maps Ghostty modifier names onto System Events ones.
var appleScriptModifiers = map[string]string{
	"super":   "command down",
	"cmd":     "command down",
	"command": "command down",
	"ctrl":    "control down",
	"control": "control down",
	"alt":     "option down",
	"opt":     "option down",
	"option":  "option down",
	"shift":   "shift down",
}

// modifierOrder keeps generated scripts stable regardless of how the chord was
// written.
var modifierOrder = []string{"command down", "control down", "option down", "shift down"}

// appleScriptKeyCodes covers the keys System Events cannot express as a
// keystroke literal.
var appleScriptKeyCodes = map[string]int{
	"return": 36, "enter": 76, "tab": 48, "space": 49, "delete": 51,
	"escape": 53, "esc": 53, "backspace": 51, "forward_delete": 117,
	"home": 115, "end": 119, "page_up": 116, "page_down": 121,
	"left": 123, "right": 124, "down": 125, "up": 126,
	"f1": 122, "f2": 120, "f3": 99, "f4": 118, "f5": 96, "f6": 97,
	"f7": 98, "f8": 100, "f9": 101, "f10": 109, "f11": 103, "f12": 111,
}

// keybindToAppleScript turns a Ghostty chord such as "super+]" into the System
// Events statement that reproduces it. It reports an error rather than
// emitting AppleScript that would fail at run time.
func keybindToAppleScript(chord string) (string, error) {
	if strings.TrimSpace(chord) == "" {
		return "", fmt.Errorf("empty keybind")
	}
	// "+" is both the separator and a possible key, so peel the key off the end
	// before splitting the modifiers.
	var key, modPart string
	switch {
	case chord == "+":
		key = "+"
	case strings.HasSuffix(chord, "++"):
		key, modPart = "+", strings.TrimSuffix(chord, "++")
	default:
		if i := strings.LastIndex(chord, "+"); i >= 0 {
			key, modPart = chord[i+1:], chord[:i]
		} else {
			key = chord
		}
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("keybind %q has no key", chord)
	}

	seen := make(map[string]bool)
	if modPart != "" {
		for _, m := range strings.Split(modPart, "+") {
			name, ok := appleScriptModifiers[strings.ToLower(strings.TrimSpace(m))]
			if !ok {
				return "", fmt.Errorf("unsupported modifier %q in keybind %q", m, chord)
			}
			seen[name] = true
		}
	}
	var mods []string
	for _, name := range modifierOrder {
		if seen[name] {
			mods = append(mods, name)
		}
	}

	var using string
	switch len(mods) {
	case 0:
	case 1:
		using = " using " + mods[0]
	default:
		using = " using {" + strings.Join(mods, ", ") + "}"
	}

	if code, ok := appleScriptKeyCodes[strings.ToLower(key)]; ok {
		return fmt.Sprintf("key code %d%s", code, using), nil
	}
	if len([]rune(key)) != 1 {
		return "", fmt.Errorf("unsupported key %q in keybind %q", key, chord)
	}
	return fmt.Sprintf("keystroke %q%s", key, using), nil
}
