# osc8wrap

A CLI tool that wraps any command and converts file paths, URLs, and symbol names in output to clickable [OSC 8 hyperlinks](https://gist.github.com/egmontkob/eb114294efbcd5adb1944c9f3cb5feda).

[![Demo](https://img.youtube.com/vi/GP5TwKnCzhQ/maxresdefault.jpg)](https://www.youtube.com/watch?v=GP5TwKnCzhQ)

[▶ Watch demo](https://www.youtube.com/watch?v=GP5TwKnCzhQ)

## Installation

### From source

```bash
go install github.com/mash/osc8wrap@latest
```

### Binary releases

Download from [GitHub Releases](https://github.com/mash/osc8wrap/releases).

## Usage

```bash
osc8wrap [options] <command> [args...]
<other command> | osc8wrap [options]
osc8wrap sessions [--format FMT] [query...]
osc8wrap open <id> [--print]
osc8wrap preview <id> [query...]
```

### Options

- `--scheme=NAME` - URL scheme for file links (default: `file`)
- `--terminator=TYPE` - OSC8 string terminator: `st` for ESC \ (default, ECMA-48), `bel` for BEL 0x07 (legacy xterm)
- `--domains=LIST` - Comma-separated domains to linkify without `https://` (default: `github.com`)
- `--no-resolve-basename` - Disable basename resolution (default: enabled)
- `--exclude-dir=DIR,...` - Directories to exclude from basename search (default: `vendor,node_modules,.git,__pycache__,.cache`)
- `--no-symbol-links` - Disable symbol linking (default: enabled when scheme != `file`)

Options can also be set via environment variables. CLI flags take precedence.

| Flag                    | Environment Variable             |
| ----------------------- | -------------------------------- |
| `--scheme`              | `OSC8WRAP_SCHEME`                |
| `--terminator`          | `OSC8WRAP_TERMINATOR`            |
| `--domains`             | `OSC8WRAP_DOMAINS`               |
| `--no-resolve-basename` | `OSC8WRAP_NO_RESOLVE_BASENAME=1` |
| `--exclude-dir`         | `OSC8WRAP_EXCLUDE_DIRS`          |
| `--no-symbol-links`     | `OSC8WRAP_NO_SYMBOL_LINKS=1`     |

### Examples

```bash
# Make grep results clickable
osc8wrap grep -rn "TODO" .

# Make Claude Code output clickable
osc8wrap claude

# Use vscode:// scheme to open files in VS Code at specific line
osc8wrap --scheme=vscode go test ./...

# Set default scheme via environment variable
export OSC8WRAP_SCHEME=cursor
osc8wrap go test ./...

# Pipe mode (auto-detected when stdin is not a terminal)
grep -rn "TODO" . | osc8wrap
cat build.log | osc8wrap --scheme=vscode

# Add to ~/.zshrc to always wrap claude and codex
alias claude='osc8wrap --scheme=cursor claude'
alias codex='osc8wrap --scheme=cursor codex'

# List every Claude Code session, running or finished (see Sessions below)
osc8wrap sessions
```

## What it does

- Detects file paths (absolute and relative) in command output
- Detects `https://` URLs
- Converts them to OSC 8 hyperlinks that work in supported terminals
- Runs commands through a PTY, so colors and interactive programs work
- Supports pipe mode for processing output from other commands
- Preserves existing ANSI escape sequences (colors, cursor control, etc.)
- **Rescues broken OSC 8 links**: some tools (notably Claude Code) wrap file references in OSC 8 hyperlinks with schemeless relative URIs like `models/foo.go` — valid-looking markup, but terminals silently refuse to open them, and worse, the broken wrapper prevents terminal-side text detectors from rescuing the path. osc8wrap detects these malformed links, strips the outer wrapper, and re-processes the inner text so the path becomes clickable again. Valid OSC 8 links (with proper schemes like `file://`, `https://`, `cursor://`) pass through untouched.

### Supported patterns

| Pattern              | Example                          |
| -------------------- | -------------------------------- |
| Absolute path        | `/path/to/file.go`               |
| Home directory path  | `~/src/project/main.go`          |
| With line number     | `/path/to/file.go:42`            |
| With line and column | `/path/to/file.go:42:10`         |
| With line range      | `/path/to/file.go:10-20`         |
| Relative path        | `./src/main.go:10`               |
| Extensionless path   | `./README`, `/path/to/LICENSE`   |
| \*file names         | `Makefile`, `Dockerfile`         |
| Git diff paths       | `a/src/main.go`, `b/src/main.go` |
| HTTPS URL            | `https://example.com/docs`       |

Paths are only linked if they exist (files or directories). Extensionless files are supported when they have a path prefix (`/`, `./`, `../`, `~/`) or end with `file` (e.g., Makefile, Dockerfile, Gemfile). Git diff `a/` and `b/` prefixes are automatically stripped when resolving paths.

### Basename resolution

When a path like `main.go:10` doesn't exist relative to the current directory, osc8wrap searches for the file in the project and creates a link to the matching file.

**How it works:**

1. On startup, osc8wrap builds a file index in the background
   - Walks the filesystem, skipping excluded directories
   - In git repositories: paths listed in `.gitignore` are automatically excluded
2. When a path doesn't exist at the literal location, the index is consulted
3. Files are matched by basename, then filtered by path suffix if the input contains `/`
4. When multiple files match, the most recently modified file is selected

**Examples:**

| Input          | Actual file              | Result                          |
| -------------- | ------------------------ | ------------------------------- |
| `main.go:10`   | `src/main.go`            | Links to `src/main.go`          |
| `to/file.go:5` | `path/to/file.go`        | Links via suffix match          |
| `file.go:1`    | `foo/file.go`, `bar/file.go` | Links to most recently modified |

**Notes:**

- Resolution only occurs when the literal path doesn't exist
- If the index isn't ready yet, unresolved paths are left as plain text
- Disable with `--no-resolve-basename` for faster startup on large codebases

### Editor schemes

By default, file links use the `file://` scheme. To open files directly in your editor at the specific line, use an editor-specific scheme:

| Scheme | URL format                    |
| ------ | ----------------------------- |
| file   | `file://hostname/path`        |
| vscode | `vscode://file/path:line:col` |
| cursor | `cursor://file/path:line:col` |
| zed    | `zed://file/path:line:col`    |

Any scheme name is accepted and will be formatted as `{scheme}://file{path}:{line}:{col}`.

### Symbol links

When using an editor scheme (not `file`), osc8wrap detects symbol names in ANSI-styled text (colored, bold, etc.) and converts them to clickable links that open the symbol definition in your editor.

**How it works:**

- Only activates inside SGR-styled text segments (e.g., colored compiler output, Claude Code, Codex CLI)
- Detects identifiers with 3+ characters (letters, digits, underscores)
- Links to `{scheme}://maaashjp.symbol-opener?symbol=NAME&cwd=CWD`
- If followed by `()`, adds `&kind=Function` to the URL

**Requirements:**

- Install the [symbol-opener](https://github.com/mash/symbol-opener) VS Code/Cursor extension
- The extension uses LSP to resolve symbol definitions

**Example:**

```bash
# Symbol linking works with Claude Code output (which uses colored text)
$ osc8wrap --scheme=cursor claude "what does NewLinker do?"
# "NewLinker" in Claude's response becomes a clickable link to the definition

# Plain text without ANSI styling is NOT processed for symbols
$ echo "NewLinker" | osc8wrap --scheme=cursor
# "NewLinker" is NOT linked (no SGR styling)
```

Disable with `--no-symbol-links` if you don't need this feature.

## Sessions

```bash
osc8wrap sessions
```

Lists every Claude Code session on this machine — the ones running under osc8wrap right now and the ones that have finished — one record per line. Live sessions come first and finished ones after, each group newest first, with the sessions waiting on you at the very top.

The live half comes from osc8wrap's own registry joined with the metadata Claude publishes in `~/.claude/sessions/` (or `$CLAUDE_CONFIG_DIR/sessions` when that variable is set): the session's name, whether it is busy or waiting, and what it is waiting for. The finished half comes from Claude's transcripts under `~/.claude/projects/` (likewise `$CLAUDE_CONFIG_DIR`), read a few dozen lines at a time — the session UUID, the directory it started in, the git branch it was on, and its first prompt. A session that is both — running now and already has a transcript — appears once.

There is no picker. `sessions` writes records and nothing else; the interactive part belongs to [fzf](https://github.com/junegunn/fzf) and a shell function, and the two subcommands below act on the id fzf hands back:

- `osc8wrap open <id>` — jump to the session's window if it is still running, resume it with `claude --resume` if it has finished.
- `osc8wrap preview <id>` — render one session for fzf's preview pane.

### The rc function

```sh
# osc8wrap: pick a Claude Code session -- running now or long finished -- and open it
ocs() {
  local line id
  line=$(fzf </dev/null \
    --query "$*" --disabled --delimiter=$'\t' --with-nth=1 --highlight-line --no-hscroll \
    --prompt 'fulltext> ' \
    --preview 'osc8wrap preview {2} {q}' \
    --preview-window 'right,60%,wrap,<100(down,60%,wrap)' \
    --bind 'ctrl-/:toggle-preview' \
    --bind 'start,change:reload:osc8wrap sessions {q}') || return
  [ -n "$line" ] || return
  id=${line#*$'\t'}; id=${id%%$'\t'*}   # display=1, id=2, cwd=3
  osc8wrap open "$id"
}
```

`--disabled` turns fzf's own filtering off and hands every keystroke to `sessions`, so what you type is matched against the conversations rather than against the display column. Letting fzf filter as well would be wrong, not merely redundant: the query ANDs its terms across the whole conversation while the excerpt in the display column only shows the neighbourhood of the first one, so fzf would hide rows that matched. The rows arrive in `sessions` order — newest first — and stay in it.

An empty query is not a special case: `{q}` is empty until the first keystroke, and `sessions` with no query lists everything. So the bind is the whole story — no fallback command, nothing to silence. `ocs` with no arguments lists everything, and `ocs migration` opens straight into a search for it.

### Output format

`--format` takes a preset or a Go [text/template](https://pkg.go.dev/text/template).

| Preset | Output |
| ------ | ------ |
| `fzf` (default) | Three tab-separated fields: a human display column, the id, the cwd. |
| `json` | An indented JSON array (`[]` when there is nothing to list). |

The display column of the `fzf` preset is `TAG  date  repo[/worktree] (branch)  summary  (waiting for)  » match`, the last part only under a query. The excerpt goes inside field 1 rather than into a fourth field, so that the field numbering stays put and fzf keeps searching it with `--with-nth=1`. Field 2 being the id and field 3 the cwd is a contract shared with the author's sibling tool ccsearch, so one rc-function shape drives both tools.

Template fields:

| Field | Meaning |
| ----- | ------- |
| `.ID` | The token `open` and `preview` accept: the Claude session UUID, or the decimal wrapper pid for a live session that has no UUID yet. |
| `.Cwd` | The directory the session started in — the only one `claude --resume` works from. |
| `.Tag` | `WAIT`, `BUSY`, `IDLE`, `LIVE` or `done`. A word, not a glyph, so it can be typed as a filter. |
| `.Live` | Boolean: an osc8wrap wrapper is still hosting this session. |
| `.Status` | Claude's own status for a live session (`waiting`, `busy`, `idle`), empty otherwise. |
| `.WaitingFor` | What a waiting session is waiting for. |
| `.Repo` | Basename of the repository root. |
| `.Worktree` | Basename of the linked worktree, empty in a main checkout. |
| `.Branch` | Branch name, or a short SHA on a detached HEAD. |
| `.Root` | Absolute repository root. |
| `.Prompt` | The session's first real user prompt. Empty is normal. |
| `.Summary` | `.Prompt`, else `.Name`, else `.Command` — osc8wrap wraps non-Claude commands too, and they must not render a blank column. |
| `.Name` | Claude's own label for a live session. |
| `.Date` | `2006-01-02 15:04`, from Claude's `updatedAt` when live and the transcript mtime otherwise. |
| `.PID` | Decimal wrapper pid, empty for a finished session. |
| `.Command` | The wrapped argv of a live session. |
| `.Match` | Where in the conversation a filtered row hit: an excerpt around the first hit. Empty when nothing was searched for, and also when the row matched on its location rather than on anything that was said. |

Every field is a string or a boolean and every one is sanitized, so no template can fail on a nil and no prompt containing a tab, a newline or an escape sequence can forge a column. A custom template supplies its own record terminator:

```bash
osc8wrap sessions --format '{{.Tag}} {{.Branch}} {{.Summary}}{{"\n"}}'
osc8wrap sessions --format json | jq -r '.[] | select(.tag == "WAIT") | .cwd'
```

A `--format` that does not resolve — an unknown preset, a template that does not parse — fails before a single byte reaches stdout. Both `--format json` and `--format=json` work.

### Full-text search

```bash
osc8wrap sessions ratchet migration
osc8wrap sessions 'ratchet migration'
```

Every argument that is not an option is part of the query: they are joined with spaces and split again on whitespace, so the two lines above are one search either way. No arguments at all — or a query that is nothing but whitespace — filters nothing and lists everything, which is what lets a picker bind the query to every keystroke and pass the empty one straight through. A term of your own that starts with `-` goes after `--`, which ends the options.

The display column carries the first prompt, which says how a session *started*. The query searches what was said in it, and where it was said. Only the sessions matching the query are listed, and a session that hit in its conversation gains a `.Match` excerpt — the text around the hit — which the `fzf` preset appends to the display column after a `»`.

Each term is looked for in **two places**:

- the **conversation**: the message text of the user and assistant turns, what you typed and what Claude wrote back. Not tool output, not thinking blocks, not the contents of the files the session read or wrote — those are the bulk of a transcript and none of them are what you remember a session by.
- the **location**: the session's cwd, repository root, worktree, branch, and the `repo/worktree` basenames the display column shows.

A term matches when **either** place holds it; a session is listed when **every** term matched, wherever each one came from. So `osc8wrap sessions osc8wrap ratchet` is "the session in the osc8wrap repo where ratchets came up" — the repo name is nowhere in the conversation and the word "ratchet" is nowhere in the path, and neither half of the session answers the query alone. Terms are whitespace-separated and matched case-insensitively as substrings; it is not a phrase search, and there is no ordering requirement. A mistyped option before `--` is still an error rather than a term, so `--fromat json` cannot come back as a confident empty listing.

The location deliberately stops short of the tag: `done`, `wait`, `busy` and `live` are **not** searched. `osc8wrap sessions done` would otherwise list every finished session on the machine, which is a filter that hides nothing while looking like it filtered — worse than not being able to type it.

A row that matched only on its location carries no `.Match`, and that is the honest answer rather than a gap: the excerpt says *where in the conversation* the hit was, and for that row there was none.

A live session is filtered like any other, and so is dropped when nothing about it matches — live rows sort to the top, so keeping them would leave the sessions you filtered *out* sitting above the ones you wanted. The live session that has no UUID yet has no transcript to search, but it does have a cwd, a repository and a branch, so it matches on those and is dropped only when a term needs a conversation it does not have.

`osc8wrap preview <id> [query...]` takes the same query: when it matches, the pane shows the matching region of the conversation instead of the session's head or tail, so the row explains itself — the turn the query hit, and above it the prompt that led to it, since the hit alone is usually a long answer with your term buried in it and shows what was said rather than what was asked. When it does not match — which is every prefix of a query you are still typing — the pane falls back to the usual turns rather than blinking empty. It still reads exactly one transcript either way.

The two fit together as the whole picker: the rc function above binds the query to every keystroke instead of letting fzf filter anything.

```sh
    --bind 'start,change:reload:osc8wrap sessions {q}' \
```

The single quotes matter: `{q}` has to reach fzf unexpanded, and fzf then shell-quotes the query when it substitutes it, so a query containing spaces, quotes or `$(...)` is passed as one argument rather than run. `start` is what fills the list before anything is typed, which is also why the rc function feeds fzf from `/dev/null` — piping a listing in as well would show a stale list for the moment before the first reload replaced it.

A full-corpus search per keystroke sounds like the expensive choice and is not. Over 474 sessions and 939 MB of transcripts on the author's machine, warm cache: the plain listing takes 90 ms, a body-only query like `sessions prefilter` 165 ms, and a two-term mixed query like `sessions osc8wrap prefilter` 185 ms. The raw line is used as a prefilter so only the lines that could match are decoded, and the files are searched concurrently.

Searching the location as well makes those queries *cheaper*, not dearer, because the two halves are done in that order: the location is consulted first, and only the terms it did **not** already satisfy are looked for in the transcript. A session whose every term is answered by its path is never opened at all — `sessions nature-server` matches 181 rows on their location and reads not one of their bodies, which is why it comes in at 130–160 ms, under the body-only query it replaces. Naming a repository narrows the corpus *before* the expensive scan instead of after it.

### Opening a session

```bash
osc8wrap open <id>
osc8wrap open <id> --print
```

If the session is still running, osc8wrap brings its Ghostty tab or split to the front. If it has finished, osc8wrap runs `claude --resume <id>` in place — stdio inherited, exit code propagated — from the directory the session **started** in; resuming from a directory it merely `cd`'d into later yields "No conversation found". When that directory no longer exists, `open` refuses and prints the command rather than resuming from the wrong place.

`--print` writes the resume command and runs nothing:

```console
$ osc8wrap open 13e5c0de-... --print
(cd '/Users/you/src/project' && claude --resume '13e5c0de-...')
```

Both values are quoted, and a value carrying a control character — macOS lets you create a directory whose name contains an ESC, and Claude records whatever directory it was started in — switches to ANSI-C quoting (`$'…'`) instead. The escape then reaches your shell as text rather than reaching your terminal as a command, at the price of a form bash and zsh understand and POSIX `sh` does not. The messages `open` prints have their control characters stripped for the same reason.

### Previewing a session

```bash
osc8wrap preview <id> [query...]
```

Prints a header naming the session — tag, repo, worktree, branch, date, cwd, id — and then its turns: the **first** ones for a finished session, because you are identifying it, and the **last** ones for a live one, because you are checking on it. Trailing words are fzf's `{q}`: when they match somewhere in the conversation the pane shows that region instead — the matching turn, preceded by the prompt that led to it — and when they do not it shows the usual turns. The header is never displaced; it answers a question the query did not replace. What claude is doing has no line of its own: the tag at the head of the header already says it (`WAIT`, `BUSY`, `IDLE`, `LIVE`, `done`), and what a waiting session is blocked on is in the listing row you are sitting on.

It reads one transcript, never the whole corpus: fzf respawns it on every keystroke that moves the highlight, and a single invocation costs milliseconds. A session with no readable transcript prints the header and a note and still exits 0, so the pane shows what is known rather than an fzf error. Every string taken from a transcript has its control characters stripped, so a turn containing an escape sequence cannot repaint the pane. `NO_COLOR` suppresses styling.

### Re-wrapping on resume

`open` on a finished session runs a bare `claude`, so the resumed session is **not** registered with osc8wrap: it will not appear in the live half of the listing, and you cannot jump to it. If that matters more to you than the extra layer, replace the last line of `ocs` with this — it reads field 3, the cwd, and wraps the resume itself (add `cwd` to the `local` declaration):

```sh
  id=${line#*$'\t'}; id=${id%%$'\t'*}
  cwd=${line##*$'\t'}
  case $line in
    done*) (cd "$cwd" && osc8wrap claude --resume "$id") ;;  # finished: re-wrap it
    *)     osc8wrap open "$id" ;;                            # still running: jump to it
  esac
```

**The `case` is not decoration.** The display column starts with the tag, so `done*` is exactly the finished rows, and only those may be resumed. Resuming a row that is still live starts a *second* Claude Code on the same session: two processes writing one transcript, two wrappers registered, and both publishing the same UUID — so the listing shows that session twice and `osc8wrap open <uuid>` then finds two records for one id and refuses to open either. The unconditional version of this recipe (resume whatever is under the cursor) corrupts the listing the first time you use it on a live row, which is the common case: live rows sort to the top, where the cursor starts.

### What it can and cannot do

**The live half only sees sessions started under osc8wrap.** Each osc8wrap process records itself in `$XDG_STATE_HOME/osc8wrap/sessions/` (`~/.local/state/osc8wrap/sessions/` by default) while it runs, and removes the file when it exits; entries whose process has died are pruned on the next listing. A `claude` you started without osc8wrap in front of it is not shown as live, and there is no way for osc8wrap to discover it — nothing is scanning your terminals. Wrap the command (`osc8wrap claude`, or an alias) and it becomes jumpable. This does not apply to the finished half: those sessions come from Claude's transcripts and are listed however they were started.

**A live session can briefly appear twice.** The two halves are joined on the session UUID, and Claude publishes its UUID a moment after it starts. In that sub-second window the registry entry and the transcript have nothing to join on, so both are listed. The alternative — guessing at the pairing from the directory and the clock — is worse.

**Jumping to a session is macOS + [Ghostty](https://ghostty.org/) only.** Ghostty exposes no URL scheme, no AppleScript dictionary and no CLI for focusing a surface, so osc8wrap drives macOS Accessibility: it tags the target window by setting its title, then walks Ghostty's windows, tabs and splits until the title matches. That takes a fraction of a second and briefly repaints the target window's title, which is restored afterwards. Both ends have to be Ghostty: `osc8wrap open` must itself run in Ghostty (it checks `TERM_PROGRAM` and the installed app), and the session you pick must have been started in one, since it is the `GHOSTTY_SURFACE_ID` recorded at that moment that identifies the surface. Elsewhere — Linux, or another terminal on macOS — listing, previewing and resuming still work and only the jump is unavailable, with the reason printed when you pick a live session.

**The Accessibility permission belongs to the host terminal, not to osc8wrap.** macOS attributes the permission to the application that owns the process tree, so it is *Ghostty* (or whichever terminal application you launched osc8wrap from) that must be enabled under System Settings → Privacy & Security → Accessibility. Granting it to the `osc8wrap` binary is not a thing macOS offers. Without it the jump fails with an osascript error and nothing moves.

### Note on the subcommand names

`sessions`, `open` and `preview` are recognized only as the first non-flag argument. To wrap a program that is itself called one of those, put it after `--` — which matters most for `open`, since macOS ships a real `open(1)`:

```bash
osc8wrap -- open .            # runs macOS open(1) on the current directory
osc8wrap open 13e5c0de-...    # the subcommand
osc8wrap -- sessions ratchet  # runs a program named "sessions"
osc8wrap sessions ratchet     # the subcommand
```

## Terminal support

See [OSC 8 adoption in terminal emulators](https://github.com/Alhadis/OSC8-Adoption/) for a list of supported terminals.

## License

MIT
