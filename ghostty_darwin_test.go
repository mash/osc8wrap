package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fakeOsaRunner captures scripts instead of running them. outs is consumed one
// entry per run, which is what lets a test describe a whole locate/select or
// locate/locate/walk sequence; out is the answer once outs is exhausted.
type fakeOsaRunner struct {
	scripts []string
	outs    []string
	out     string
	err     error
}

func (f *fakeOsaRunner) run(script string) (string, error) {
	f.scripts = append(f.scripts, script)
	if len(f.outs) > 0 {
		out := f.outs[0]
		f.outs = f.outs[1:]
		return out, f.err
	}
	return f.out, f.err
}

// newTestNavigator wires a navigator that cannot touch the user's windows: the
// AppleScript is captured instead of run, and the keybind is fixed so no
// Ghostty binary is consulted.
func newTestNavigator(runner osaRunner) *ghosttyNavigator {
	return &ghosttyNavigator{
		runner:       runner,
		splitKeybind: func() string { return defaultSplitKeybind },
		writeTitle:   writeTerminalTitle,
	}
}

// fakeTTY stands in for /dev/ttysNNN: writeTerminalTitle opens it O_WRONLY
// without O_CREATE, so a regular file behaves the same way.
func fakeTTY(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ttys000")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const goldenFocusScript = `on walkSplits(theTag)
	tell application "System Events"
		tell process "Ghostty"
			set areaTotal to 0
			repeat with el in (entire contents of window 1)
				try
					if class of el is scroll area then set areaTotal to areaTotal + 1
				end try
			end repeat
			repeat (areaTotal - 1) times
				keystroke "]" using command down
				my pollUntil("title", theTag, 5)
				if (name of window 1) is theTag then return true
			end repeat
		end tell
	end tell
	return false
end walkSplits

on checkNow(what, target)
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
end pollUntil

on confirmFront(theTag)
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
end finish

on restoreFront(startPos, startTab)
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
end restoreFront

set theTag to "osc8wrap⟦123-ABCDEFGHIJ⟧"
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

func TestBuildFocusScript(t *testing.T) {
	got := buildFocusScript("osc8wrap⟦123-ABCDEFGHIJ⟧", `keystroke "]" using command down`)
	if got != goldenFocusScript {
		t.Errorf("script changed:\ngot:\n%s\n\nwant:\n%s", got, goldenFocusScript)
	}
}

const goldenLocateSelectScript = `on checkNow(what, target)
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
end pollUntil

on confirmFront(theTag)
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
end finish

set theTag to "osc8wrap⟦123-ABCDEFGHIJ⟧"
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
if foundWindow is 0 then return "miss"
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

func TestBuildLocateSelectScript(t *testing.T) {
	got := buildLocateSelectScript("osc8wrap⟦123-ABCDEFGHIJ⟧")
	if got != goldenLocateSelectScript {
		t.Errorf("script changed:\ngot:\n%s\n\nwant:\n%s", got, goldenLocateSelectScript)
	}
}

// The locate half exists to find the target without the user seeing anything
// happen. Since the locate and the select now share one script, "perturbs
// nothing" becomes an ordering claim: everything that moves a window must come
// after the scan, and the miss path must return before reaching any of it.
func TestBuildLocateSelectScript_LocatesBeforePerturbingAnything(t *testing.T) {
	script := buildLocateSelectScript("tag")

	miss := strings.Index(script, `if foundWindow is 0 then return "`+locateMissResult)
	if miss < 0 {
		t.Fatal("the script cannot report a miss, so a background split can never reach the walk")
	}
	// The handlers sit above the scan and are only ever called from the select
	// half, so the ordering is measured over the script body alone.
	body := script[strings.Index(script, "\nset theTag to "):]
	scan := strings.Index(body, "title of radio buttons")
	if scan < 0 {
		t.Fatal("the script does not read background tab titles")
	}
	missInBody := strings.Index(body, "if foundWindow is 0 then return")

	for _, forbidden := range []string{
		"AXRaise",
		"AXPress",
		"AXFocused",
		"click ",
		"keystroke",
		"key code",
		"activate",
		"set frontmost",
		"select ",
	} {
		if i := strings.Index(body, forbidden); i >= 0 && i < missInBody {
			t.Errorf("the script perturbs the user's windows before it knows where the tag is: %q", forbidden)
		}
	}
	if scan > missInBody {
		t.Error("the tab bar is read after the miss is reported")
	}
	// "container" is a reserved word in System Events; using it as an
	// identifier fails with error -10006.
	if strings.Contains(script, "container") {
		t.Error("script uses the reserved identifier container")
	}
	// Saving a window reference captures it by name and goes stale with -1728.
	savesWindow := regexp.MustCompile(`(?m)^\s*set\s+\w+\s+to\s+window\b`)
	if loc := savesWindow.FindString(script); loc != "" {
		t.Errorf("script saves a window reference: %q", strings.TrimSpace(loc))
	}
	// The tag has to land in the accessibility tree before it can be read, and
	// the scan retries rather than waiting a fixed time for it.
	if !strings.Contains(body, "repeat 3 times") {
		t.Error("the scan does not retry, so it can only see a tag that had already landed")
	}
}

// Reading one radio button at a time costs an accessibility round trip per tab;
// the bulk read costs one per window and is what makes the scan independent of
// how many tabs are open.
func TestBuildLocateSelectScript_ReadsTabTitlesInBulk(t *testing.T) {
	script := buildLocateSelectScript("tag")

	if !strings.Contains(script, "set tabNames to title of radio buttons of tab group 1 of window wi") {
		t.Error("the script does not read a window's tab titles in one go")
	}
	if strings.Contains(script, "title of radio button ti") {
		t.Error("the script reads tab titles one element at a time")
	}
	// The bulk list is positional and 1-indexed, which is the index AXPress
	// needs, so the item index must be what is remembered.
	if !strings.Contains(script, "set tabName to (item ti of tabNames) as text") {
		t.Error("the script does not index the bulk list positionally")
	}
	if !strings.Contains(script, "set foundTab to ti") {
		t.Error("the script does not remember the tab index it matched")
	}
}

// The point of locating first is that exactly one tab is ever touched. A select
// half that loops over tabs, or that presses a tab other than the one the scan
// named, has quietly reintroduced the visible walk.
func TestBuildLocateSelectScript_TouchesOneTabOnly(t *testing.T) {
	script := buildLocateSelectScript("tag")

	if n := strings.Count(script, "AXPress"); n != 1 {
		t.Errorf("the script presses %d times, want exactly 1", n)
	}
	if !strings.Contains(script, `perform action "AXPress" of radio button foundTab of tab group 1 of window 1`) {
		t.Error("the script does not press the tab the scan named")
	}
	// AXFocused on a Ghostty surface reports success and moves nothing.
	if strings.Contains(script, "AXFocused") {
		t.Error("the script relies on AXFocused, which silently does nothing")
	}

	// AXRaise re-sorts the window list, so the located index names that window
	// only in the raise itself; everything after must address it as window 1.
	const raiseStmt = `perform action "AXRaise" of window foundWindow`
	raise := strings.Index(script, raiseStmt)
	if raise < 0 {
		t.Fatal("the script never raises the located window")
	}
	after := script[raise+len(raiseStmt):]
	if strings.Contains(after, "of window foundWindow") {
		t.Error("script addresses the window by its pre-raise index after raising it")
	}
	// Only the select half may loop; the scan's loops all sit before the miss.
	selectHalf := script[strings.Index(script, "if foundWindow is 0 then return"):]
	if strings.Contains(selectHalf, "repeat") {
		t.Error("the select half loops; it was handed the exact window and tab")
	}

	// A located tab that was never brought to the front is still a failure.
	if !strings.Contains(script, `return finish(theTag, "window " & (foundWindow as text) & " tab " & (foundTab as text))`) {
		t.Error("the script does not funnel its result through the confirmation handler")
	}
	if !strings.Contains(script, "on confirmFront(theTag)") {
		t.Error("the script does not carry the confirmation handler")
	}
}

// Locating and acting on what was located is one osascript, not two: a second
// spawn costs ~67 ms of process startup plus another accessibility handshake
// for a phase that has nothing to say to Go in between.
func TestBuildLocateSelectScript_IsOneInvocation(t *testing.T) {
	script := buildLocateSelectScript("tag")

	if !strings.Contains(script, "title of radio buttons") {
		t.Error("the merged script does not locate")
	}
	if !strings.Contains(script, "AXPress") {
		t.Error("the merged script does not select")
	}
	if !strings.Contains(script, "on confirmFront(theTag)") {
		t.Error("the merged script does not confirm")
	}
	// The only result that goes back to Go before anything has moved.
	if !strings.Contains(script, `then return "`+locateMissResult+`"`) {
		t.Errorf("the merged script cannot report %q", locateMissResult)
	}
}

// Every fixed delay was waiting for something observable. Sleeping for it costs
// the worst case every time; polling for it costs the worst case only when the
// UI really is that slow.
func TestScriptsPollInsteadOfSleeping(t *testing.T) {
	scripts := map[string]string{
		"locate/select": buildLocateSelectScript("tag"),
		"walk":          buildFocusScript("tag", `keystroke "]" using command down`),
	}
	// 0.02 is the poll's own sleep between attempts; anything longer is a
	// fixed wait that has crept back in.
	fixedDelay := regexp.MustCompile(`delay (?:0\.(?:0[3-9]|[1-9]\d*)|[1-9])`)

	for name, script := range scripts {
		if d := fixedDelay.FindString(script); d != "" {
			t.Errorf("%s script still sleeps a fixed %q instead of polling", name, d)
		}
		if !strings.Contains(script, "on pollUntil(what, target, maxTicks)") {
			t.Errorf("%s script does not carry the bounded poll handler", name)
		}
		// The poll must be bounded, or a target that never appears hangs the
		// whole invocation.
		if !strings.Contains(script, "repeat maxTicks times") {
			t.Errorf("%s script's poll is not bounded", name)
		}
	}

	sel := scripts["locate/select"]
	// After the activate, wait for the app to actually be frontmost.
	if !strings.Contains(sel, `tell application "Ghostty" to activate`+"\n"+`pollUntil("frontmost", true, 7)`) {
		t.Error("the script does not poll for Ghostty becoming frontmost after activating it")
	}
	// After the press, wait for the pressed tab's title to arrive.
	press := strings.Index(sel, "AXPress")
	if press < 0 {
		t.Fatal("the script never presses a tab")
	}
	if !strings.Contains(sel[press:], `my pollUntil("title", theTag, 5)`) {
		t.Error("the script does not poll for the pressed tab's title")
	}
	// And confirmFront waits for its own raise the same way.
	confirm := sel[strings.Index(sel, "on confirmFront("):strings.Index(sel, "end confirmFront")]
	if !strings.Contains(confirm, `my pollUntil("title", theTag, 5)`) {
		t.Error("confirmFront sleeps instead of polling for the raise it just performed")
	}
}

// A walk that visits every tab of every window and finds nothing must not
// abandon the user on whichever tab it stopped at.
func TestBuildFocusScript_RestoresTheStartingTabWhenNothingIsFound(t *testing.T) {
	script := buildFocusScript("tag", `keystroke "]" using command down`)

	const restoreCall = "restoreFront(startPos, startTab)"
	if !strings.Contains(script, "\n"+restoreCall) {
		t.Fatal("the walk never restores the tab it started from")
	}

	// The starting position has to be read before the first raise, since the
	// raise is what destroys the ordering it would otherwise be recovered from.
	capture := strings.Index(script, "set startPos to position of window 1")
	if capture < 0 {
		t.Fatal("the walk never records where it started")
	}
	body := windowLoopBody(t, script)
	if capture > strings.Index(script, body) {
		t.Error("the starting position is recorded after the walk has already begun")
	}
	if !strings.Contains(script, "set startTab to si") {
		t.Error("the walk records no starting tab, so it can only restore the window")
	}

	// Restoring after a successful walk would undo the focus that was just
	// achieved, so the notfound path must be the only one that reaches it.
	// The handler's own "on restoreFront(...)" line is not a call, so calls are
	// counted at the start of a line.
	if n := strings.Count(script, "\n"+restoreCall); n != 1 {
		t.Errorf("the walk calls restoreFront %d times, want exactly 1", n)
	}
	if !strings.Contains(script, restoreCall+"\nreturn \"notfound\"") {
		t.Error("the restore is not confined to the notfound path")
	}
}

func TestBuildFocusScript_AvoidsAppleScriptTraps(t *testing.T) {
	script := buildFocusScript("tag", `keystroke "]" using command down`)

	// Saving a window reference captures it by name; selecting a tab renames
	// the window and the saved reference then fails with error -1728.
	savesWindow := regexp.MustCompile(`(?m)^\s*set\s+\w+\s+to\s+window\b`)
	if loc := savesWindow.FindString(script); loc != "" {
		t.Errorf("script saves a window reference: %q", strings.TrimSpace(loc))
	}
	if !strings.Contains(script, "of window 1") {
		t.Error("script never re-resolves a window inline")
	}
	// "container" is a reserved word in System Events; using it as an
	// identifier fails with error -10006.
	if strings.Contains(script, "container") {
		t.Error("script uses the reserved identifier container")
	}
	// Counting through entire contents is what makes the split and no-split
	// nesting depths equivalent.
	if !strings.Contains(script, "entire contents of window 1") {
		t.Error("script does not count scroll areas recursively")
	}
}

// windowLoopBody returns the part of the script from the window loop onwards,
// which is where a window is raised and its title compared. The handlers above
// it raise window 1 only, so they would blur every ordering assertion.
func windowLoopBody(t *testing.T, script string) string {
	t.Helper()
	loop := strings.Index(script, "repeat with wi from 1 to windowTotal")
	if loop < 0 {
		t.Fatal("script has no window loop")
	}
	return script[loop:]
}

// A window that is reported as found must have been raised first: activating
// Ghostty does not reorder its windows, so returning "found" for a background
// window whose title already matches would report success while the user keeps
// looking at whatever is in front.
func TestBuildFocusScript_RaisesBeforeMatching(t *testing.T) {
	script := buildFocusScript("tag", `keystroke "]" using command down`)
	body := windowLoopBody(t, script)

	// The raise names the window by the loop index; that is the statement the
	// ordering is measured against, so it is matched in full rather than by
	// its "AXRaise" prefix alone.
	const raiseStmt = `perform action "AXRaise" of window wi`
	raise := strings.Index(body, raiseStmt)
	if raise < 0 {
		t.Fatal("the window loop never raises the window it is visiting")
	}
	match := strings.Index(body, "(name of window")
	if match < 0 {
		t.Fatal("script never compares a window title")
	}
	if match < raise {
		t.Error("the window title is compared before the window is raised")
	}
	// Clicking a tab in a window that is still behind another one would search
	// a window the user cannot see.
	if click := strings.Index(body, "click radio button"); click >= 0 && click < raise {
		t.Error("a tab is selected before the window is raised")
	}

	// AXRaise re-sorts the process's window list, so the index it was handed no
	// longer names the raised window. Handing it that index is how the raise
	// addresses the window in the first place, so only what comes after the
	// raise statement is inspected: from there on the window must be
	// re-resolved as window 1.
	if after := body[raise+len(raiseStmt):]; strings.Contains(after, "window wi") {
		t.Error("script addresses a window by its pre-raise index after raising it")
	}
}

// Matching the tag is not by itself evidence that anything moved: the raise is
// wrapped in a try, and the accessibility window order can be stale. So a match
// is re-raised and then verified to be the frontmost window of a frontmost
// Ghostty, and a walk that cannot achieve that reports a failure instead.
func TestBuildFocusScript_ConfirmsTheMatchIsInFront(t *testing.T) {
	script := buildFocusScript("tag", `keystroke "]" using command down`)
	body := windowLoopBody(t, script)

	if strings.Contains(body, `return "`+focusFoundPrefix) {
		t.Error("the walk reports success directly instead of confirming the window is in front")
	}
	if !strings.Contains(body, "return my finish(theTag,") {
		t.Error("the walk does not funnel its matches through the confirmation handler")
	}

	start := strings.Index(script, "on confirmFront(")
	end := strings.Index(script, "end confirmFront")
	if start < 0 || end < start {
		t.Fatal("script has no confirmFront handler")
	}
	confirm := script[start:end]
	if !strings.Contains(confirm, `perform action "AXRaise" of window 1`) {
		t.Error("confirmFront does not raise the matched window again before reporting success")
	}
	if !strings.Contains(confirm, "(name of window 1) is not theTag") {
		t.Error("confirmFront does not check that the front window is the tagged one")
	}
	if !strings.Contains(confirm, "frontmost is false") {
		t.Error("confirmFront does not check that Ghostty itself is frontmost")
	}

	if !strings.Contains(script, `if confirmFront(theTag) then return "`+focusFoundPrefix) {
		t.Error("success is not gated on confirmFront")
	}
	if !strings.Contains(script, `return "`+focusNotFrontPrefix) {
		t.Error("script cannot report a match it failed to bring to the front")
	}
}

func TestKeybindToAppleScript(t *testing.T) {
	tests := []struct {
		name  string
		chord string
		want  string
	}{
		{"ghostty default", "super+]", `keystroke "]" using command down`},
		{"cmd alias", "cmd+]", `keystroke "]" using command down`},
		{"no modifier", "]", `keystroke "]"`},
		{"two modifiers", "super+shift+j", `keystroke "j" using {command down, shift down}`},
		{"modifier order is normalized", "shift+super+j", `keystroke "j" using {command down, shift down}`},
		{"all modifiers", "ctrl+alt+shift+cmd+k", `keystroke "k" using {command down, control down, option down, shift down}`},
		{"control and option aliases", "control+option+k", `keystroke "k" using {control down, option down}`},
		{"opt alias", "opt+k", `keystroke "k" using option down`},
		{"uppercase modifier", "SUPER+]", `keystroke "]" using command down`},
		{"named key", "super+right", "key code 124 using command down"},
		{"named key without modifier", "escape", "key code 53"},
		{"function key", "ctrl+f5", "key code 96 using control down"},
		{"plus as the key", "super++", `keystroke "+" using command down`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := keybindToAppleScript(tt.chord)
			if err != nil {
				t.Fatalf("keybindToAppleScript(%q): %v", tt.chord, err)
			}
			if got != tt.want {
				t.Errorf("keybindToAppleScript(%q) = %q, want %q", tt.chord, got, tt.want)
			}
		})
	}
}

func TestKeybindToAppleScript_Errors(t *testing.T) {
	tests := []struct {
		name  string
		chord string
	}{
		{"empty", ""},
		{"unknown modifier", "hyper+]"},
		{"unnamed multi-rune key", "super+brackets"},
		{"modifier without key", "super+"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := keybindToAppleScript(tt.chord)
			if err == nil {
				t.Fatalf("keybindToAppleScript(%q) = %q, want an error", tt.chord, got)
			}
		})
	}
}

func TestParseSplitNextKeybind(t *testing.T) {
	listOutput := `keybind = super+t=new_tab
keybind = super+d=new_split:right
keybind = super+]=goto_split:next
keybind = super+[=goto_split:previous
keybind = super+w=close_surface`

	tests := []struct {
		name string
		out  string
		want string
	}{
		{"default binding", listOutput, "super+]"},
		{"rebound", "keybind = ctrl+shift+n=goto_split:next", "ctrl+shift+n"},
		{"no keybind prefix", "super+n=goto_split:next", "super+n"},
		{"chord containing =", "keybind = super+equal=goto_split:next", "super+equal"},
		{"binding missing", "keybind = super+t=new_tab", defaultSplitKeybind},
		{"empty output", "", defaultSplitKeybind},
		{"prefix match is not a binding", "keybinds-are-fun = super+n=goto_split:next", defaultSplitKeybind},
		{"similar action", "keybind = super+n=goto_split:previous", defaultSplitKeybind},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSplitNextKeybind(tt.out); got != tt.want {
				t.Errorf("parseSplitNextKeybind() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The fallback chord is only useful if it can actually be typed.
func TestDefaultSplitKeybindIsExpressible(t *testing.T) {
	if _, err := keybindToAppleScript(defaultSplitKeybind); err != nil {
		t.Fatalf("default keybind %q: %v", defaultSplitKeybind, err)
	}
}

func TestFocusTag(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		tag := focusTag(74)
		if seen[tag] {
			t.Fatalf("focusTag repeated %q", tag)
		}
		seen[tag] = true
		if !strings.HasPrefix(tag, "osc8wrap⟦74-") || !strings.HasSuffix(tag, "⟧") {
			t.Fatalf("focusTag(74) = %q, want an osc8wrap⟦74-…⟧ tag", tag)
		}
	}
}

// isLocateSelectScript reports whether a captured script is the merged
// locate-and-select pass rather than the walk. The walk is the only script that
// cycles splits.
func isLocateSelectScript(script string) bool {
	return strings.Contains(script, "title of radio buttons") && !strings.Contains(script, "walkSplits")
}

// The happy path: the tab bar names the target, so exactly one tab is pressed,
// no other tab is ever selected, and it all happens in a single osascript.
func TestGhosttyNavigator_FocusLocatedAndConfirmed(t *testing.T) {
	tty := fakeTTY(t)
	runner := &fakeOsaRunner{outs: []string{"found window 2 tab 6"}}
	nav := newTestNavigator(runner)
	s := LiveSession{
		Entry: RegistryEntry{WrapperPID: 74, TTY: tty, SurfaceID: "0x1552e82d3b500c1e"},
		Name:  "osc8wrap-74",
	}

	if err := nav.Focus(s); err != nil {
		t.Fatalf("Focus: %v", err)
	}
	// One spawn, not two: locating and pressing are one operation.
	if len(runner.scripts) != 1 {
		t.Fatalf("ran %d scripts, want 1 (locate and select merged)", len(runner.scripts))
	}
	if !isLocateSelectScript(runner.scripts[0]) {
		t.Error("Focus did not read the tab bar before touching anything")
	}
	if !strings.Contains(runner.scripts[0], "osc8wrap⟦74-") {
		t.Error("the script does not carry the tag written to the tty")
	}
	// The whole point: no walk, and the press goes straight at the tab the scan
	// named.
	if strings.Contains(runner.scripts[0], "walkSplits") || strings.Contains(runner.scripts[0], "click radio button") {
		t.Error("Focus walked the tabs although the tab bar could answer")
	}
	if !strings.Contains(runner.scripts[0], `perform action "AXRaise" of window foundWindow`) ||
		!strings.Contains(runner.scripts[0], `radio button foundTab of tab group 1 of window 1`) {
		t.Errorf("the script does not act on the window and tab it located:\n%s", runner.scripts[0])
	}
	assertTitleRestored(t, tty, "osc8wrap-74")
}

// Locating the surface is not the same as focusing it. If the script found the
// tag but could not leave that window in front, the user is still looking at
// something else, so Focus must fail rather than claim success -- and it must
// not silently fall back to the walk either, which would move things around
// after having already reached the target.
func TestGhosttyNavigator_FocusLocatedButNotBroughtToFront(t *testing.T) {
	tty := fakeTTY(t)
	runner := &fakeOsaRunner{outs: []string{focusNotFrontPrefix + "window 2 tab 1"}}
	nav := newTestNavigator(runner)
	s := LiveSession{Entry: RegistryEntry{WrapperPID: 74, TTY: tty, SurfaceID: "0xabc"}, Name: "osc8wrap-74"}

	err := nav.Focus(s)
	if err == nil {
		t.Fatal("Focus succeeded although the window was never brought to the front")
	}
	if !strings.Contains(err.Error(), "window 2 tab 1") {
		t.Errorf("error does not say where the surface was found: %v", err)
	}
	if strings.Contains(err.Error(), "was not found") {
		t.Errorf("a located surface is reported as never found: %v", err)
	}
	if len(runner.scripts) != 1 {
		t.Errorf("ran %d scripts, want 1: a located-but-unfronted target must not be walked for", len(runner.scripts))
	}
	assertTitleRestored(t, tty, "osc8wrap-74")
}

// A miss means the target is a background split, whose title no tab bar can
// show. Walking is then the only way there, so Focus must fall back to it.
func TestGhosttyNavigator_FocusMissThenWalkFindsIt(t *testing.T) {
	tty := fakeTTY(t)
	runner := &fakeOsaRunner{outs: []string{"miss", "miss", "miss", "found window 1 tab 2"}}
	nav := newTestNavigator(runner)
	s := LiveSession{Entry: RegistryEntry{WrapperPID: 74, TTY: tty, SurfaceID: "0xabc"}, Name: "osc8wrap-74"}

	if err := nav.Focus(s); err != nil {
		t.Fatalf("Focus: %v", err)
	}
	if len(runner.scripts) != locatePolls+1 {
		t.Fatalf("ran %d scripts, want %d locate polls plus the walk", len(runner.scripts), locatePolls)
	}
	for i, script := range runner.scripts[:locatePolls] {
		if !isLocateSelectScript(script) {
			t.Errorf("script %d is not a locate pass", i)
		}
	}
	walk := runner.scripts[locatePolls]
	if !strings.Contains(walk, "repeat with wi from 1 to windowTotal") {
		t.Error("Focus did not fall back to the walk after the locate phase missed")
	}
	// The walk still has to be able to reach a split, which is the only reason
	// it survives at all.
	if !strings.Contains(walk, "walkSplits") {
		t.Error("the fallback walk no longer cycles splits")
	}
	assertTitleRestored(t, tty, "osc8wrap-74")
}

// When even the walk finds nothing the user must be put back where they were,
// which the walk script does for itself; Focus's job is to run that script and
// report the failure.
func TestGhosttyNavigator_FocusMissThenWalkFailsAndRestores(t *testing.T) {
	tty := fakeTTY(t)
	runner := &fakeOsaRunner{outs: []string{"miss", "miss", "miss", "notfound"}}
	nav := newTestNavigator(runner)
	s := LiveSession{Entry: RegistryEntry{WrapperPID: 74, TTY: tty, SurfaceID: "0xabc", Command: []string{"claude"}}}

	err := nav.Focus(s)
	if err == nil {
		t.Fatal("Focus succeeded although the tag was never found")
	}
	if !strings.Contains(err.Error(), "0xabc") || !strings.Contains(err.Error(), tty) {
		t.Errorf("error does not say what was searched: %v", err)
	}
	if len(runner.scripts) != locatePolls+1 {
		t.Fatalf("ran %d scripts, want %d locate polls plus the walk", len(runner.scripts), locatePolls)
	}
	if !strings.Contains(runner.scripts[locatePolls], "restoreFront(startPos, startTab)") {
		t.Error("the walk that failed cannot put the user back on the tab they started from")
	}
	// Restoration must survive a failed walk, or the tag stays on screen.
	assertTitleRestored(t, tty, "claude")
}

// claude repaints its own title constantly, so the tag can be gone by the time
// the tab bar is read. Merging locate and select does not change that: only Go
// can re-write the tag, so a miss must still come back here, be re-tagged and be
// read again. Without that, any session doing work misses every time and
// silently pays for the whole walk.
func TestGhosttyNavigator_FocusRetagsBetweenLocatePolls(t *testing.T) {
	tty := fakeTTY(t)
	runner := &fakeOsaRunner{outs: []string{"miss", "found window 1 tab 6"}}
	nav := newTestNavigator(runner)

	var titles []string
	nav.writeTitle = func(_, title string) error {
		titles = append(titles, title)
		return nil
	}
	s := LiveSession{Entry: RegistryEntry{WrapperPID: 74, TTY: tty, SurfaceID: "0xabc"}, Name: "osc8wrap-74"}

	if err := nav.Focus(s); err != nil {
		t.Fatalf("Focus: %v", err)
	}
	if len(runner.scripts) != 2 {
		t.Fatalf("ran %d scripts, want 2 (a missed poll, then a hit)", len(runner.scripts))
	}
	if !isLocateSelectScript(runner.scripts[1]) {
		t.Errorf("the second poll was not another locate pass:\n%s", runner.scripts[1])
	}

	// Two tags, one per poll, then the restored title. A single tag write means
	// the second poll read whatever claude had painted over it.
	if len(titles) != 3 {
		t.Fatalf("wrote %d titles %q, want the tag once per poll plus the restore", len(titles), titles)
	}
	if titles[0] != titles[1] || !strings.HasPrefix(titles[0], "osc8wrap⟦74-") {
		t.Errorf("the tag was not re-written before the second poll: %q", titles)
	}
	if titles[2] != "osc8wrap-74" {
		t.Errorf("the tag was left on screen: last title %q", titles[2])
	}
}

// The locate phase is an optimisation. If its own osascript fails there is
// still a walk that can find the surface, so the failure must not abort Focus.
func TestGhosttyNavigator_FocusLocateFailureFallsBackToTheWalk(t *testing.T) {
	tty := fakeTTY(t)
	runner := &fakeOsaRunner{err: errors.New("locate blew up")}
	nav := newTestNavigator(runner)
	// The walk answers through a second runner, since the first one only knows
	// how to fail.
	nav.runner = &sequencedOsaRunner{first: runner, rest: &fakeOsaRunner{out: "found window 1"}}
	s := LiveSession{Entry: RegistryEntry{WrapperPID: 74, TTY: tty, SurfaceID: "0xabc"}, Name: "osc8wrap-74"}

	if err := nav.Focus(s); err != nil {
		t.Fatalf("Focus: %v", err)
	}
	assertTitleRestored(t, tty, "osc8wrap-74")
}

// sequencedOsaRunner sends the first script to one runner and everything after
// it to another.
type sequencedOsaRunner struct {
	first osaRunner
	rest  osaRunner
	n     int
}

func (s *sequencedOsaRunner) run(script string) (string, error) {
	s.n++
	if s.n == 1 {
		return s.first.run(script)
	}
	return s.rest.run(script)
}

func TestGhosttyNavigator_FocusOsascriptError(t *testing.T) {
	tty := fakeTTY(t)
	osaErr := errors.New("execution error: Ghostty got an error (-1728)")
	nav := newTestNavigator(&fakeOsaRunner{err: osaErr})
	s := LiveSession{Entry: RegistryEntry{WrapperPID: 74, TTY: tty, SurfaceID: "0xabc"}, Name: "osc8wrap-74"}

	err := nav.Focus(s)
	if !errors.Is(err, osaErr) {
		t.Fatalf("Focus error = %v, want it to wrap the osascript failure", err)
	}
	assertTitleRestored(t, tty, "osc8wrap-74")
}

func TestGhosttyNavigator_FocusRejectsIncompleteEntries(t *testing.T) {
	tests := []struct {
		name  string
		entry RegistryEntry
	}{
		{"no surface id", RegistryEntry{WrapperPID: 74, TTY: "/dev/ttys011"}},
		{"no tty", RegistryEntry{WrapperPID: 74, SurfaceID: "0xabc"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeOsaRunner{out: "found window 1"}
			if err := newTestNavigator(runner).Focus(LiveSession{Entry: tt.entry}); err == nil {
				t.Fatal("Focus succeeded on an incomplete registry entry")
			}
			if len(runner.scripts) != 0 {
				t.Error("Focus ran AppleScript for an incomplete registry entry")
			}
		})
	}
}

func TestGhosttyNavigator_AvailableNeedsGhosttyBinary(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "ghostty")

	ok, reason := NewGhosttyNavigator().Available()
	if _, err := os.Stat(ghosttyBinary); err != nil {
		if ok {
			t.Fatalf("reported available although %s is missing", ghosttyBinary)
		}
		if !strings.Contains(reason, ghosttyBinary) {
			t.Errorf("reason does not name the missing binary: %q", reason)
		}
		return
	}
	if !ok {
		t.Fatalf("reported unavailable under Ghostty with the binary installed: %s", reason)
	}
}

// assertTitleRestored checks the last OSC 2 written to the tty carries the
// restored title rather than the tag. Writes to a regular file all start at
// offset 0, so the restored sequence sits at the front.
func assertTitleRestored(t *testing.T, tty, want string) {
	t.Helper()
	got, err := os.ReadFile(tty)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "\033]2;"+want+"\033\\") {
		t.Errorf("tty content %q does not start with the restored title %q", got, want)
	}
}
