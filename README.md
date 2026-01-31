# osc8wrap

A CLI tool that wraps any command and converts file paths and URLs in output to clickable [OSC 8 hyperlinks](https://gist.github.com/egmontkob/eb114294efbcd5adb1944c9f3cb5feda).

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
```

### Options

- `--scheme=NAME` - URL scheme for file links (default: `file`)
- `--terminator=TYPE` - OSC8 string terminator: `st` for ESC \ (default, ECMA-48), `bel` for BEL 0x07 (legacy xterm)
- `--domains=LIST` - Comma-separated domains to linkify without `https://` (default: `github.com`)
- `--no-resolve-basename` - Disable basename resolution (default: enabled)
- `--exclude-dir=DIR,...` - Directories to exclude from basename search (default: `vendor,node_modules,.git,__pycache__,.cache`)
- `--no-symbol-links` - Disable symbol linking (default: enabled when scheme != `file`)

Options can also be set via environment variables. CLI flags take precedence.

| Flag | Environment Variable |
| ---- | -------------------- |
| `--scheme` | `OSC8WRAP_SCHEME` |
| `--terminator` | `OSC8WRAP_TERMINATOR` |
| `--domains` | `OSC8WRAP_DOMAINS` |
| `--no-resolve-basename` | `OSC8WRAP_NO_RESOLVE_BASENAME=1` |
| `--exclude-dir` | `OSC8WRAP_EXCLUDE_DIRS` |
| `--no-symbol-links` | `OSC8WRAP_NO_SYMBOL_LINKS=1` |

### Examples

```bash
# Make file paths in build errors clickable
osc8wrap go build ./...

# Make grep results clickable
osc8wrap grep -rn "TODO" .

# Make Claude Code output clickable
osc8wrap claude

# Use vscode:// scheme to open files in VS Code at specific line
osc8wrap --scheme=vscode go build ./...

# Set default scheme via environment variable
export OSC8WRAP_SCHEME=cursor
osc8wrap go test ./...

# Pipe mode (auto-detected when stdin is not a terminal)
grep -rn "TODO" . | osc8wrap
cat build.log | osc8wrap --scheme=vscode
```

## What it does

- Detects file paths (absolute and relative) in command output
- Detects `https://` URLs
- Converts them to OSC 8 hyperlinks that work in supported terminals
- Runs commands through a PTY, so colors and interactive programs work
- Supports pipe mode for processing output from other commands

### Supported patterns

| Pattern              | Example                    |
| -------------------- | -------------------------- |
| Absolute path        | `/path/to/file.go`         |
| With line number     | `/path/to/file.go:42`      |
| With line and column | `/path/to/file.go:42:10`   |
| Relative path        | `./src/main.go:10`         |
| Extensionless path   | `./README`, `/path/to/LICENSE` |
| *file names          | `Makefile`, `Dockerfile`   |
| HTTPS URL            | `https://example.com/docs` |

Paths are only linked if they exist (files or directories). Extensionless files are supported when they have a path prefix (`/`, `./`, `../`) or end with `file` (e.g., Makefile, Dockerfile, Gemfile).

### Basename resolution

When a path like `main.go:10` doesn't exist relative to the current directory, osc8wrap searches for the file in the project and creates a link to the matching file.

**How it works:**

1. On startup, osc8wrap builds a file index in the background
   - In git repositories: uses `git ls-files` for fast indexing
   - Otherwise: walks the filesystem, skipping excluded directories
2. When a path doesn't exist at the literal location, the index is consulted
3. Files are matched by basename, then filtered by path suffix if the input contains `/`
4. When multiple files match, the most recently modified file is selected

**Examples:**

| Input | Actual file | Result |
| ----- | ----------- | ------ |
| `main.go:10` | `src/main.go` | Links to `src/main.go` |
| `to/file.go:5` | `path/to/file.go` | Links via suffix match |
| `file.go:1` | `a/file.go`, `b/file.go` | Links to most recently modified |

**Notes:**

- Resolution only occurs when the literal path doesn't exist
- If the index isn't ready yet, unresolved paths are left as plain text
- Disable with `--no-resolve-basename` for faster startup on large codebases

### Editor schemes

By default, file links use the `file://` scheme. To open files directly in your editor at the specific line, use an editor-specific scheme:

| Scheme   | URL format                          |
| -------- | ----------------------------------- |
| file     | `file://hostname/path`              |
| vscode   | `vscode://file/path:line:col`       |
| cursor   | `cursor://file/path:line:col`       |
| zed      | `zed://file/path:line:col`          |

Any scheme name is accepted and will be formatted as `{scheme}://file{path}:{line}:{col}`.

### Symbol links

When using an editor scheme (not `file`), osc8wrap also detects symbol names (function names, type names, etc.) and converts them to clickable links that open the symbol definition in your editor.

**How it works:**

- Detects PascalCase identifiers (e.g., `NewLinker`, `HTTPClient`)
- Detects camelCase identifiers (e.g., `getUserName`, `parseJSON`)
- Minimum 3 characters to reduce noise
- Links to `{scheme}://mash.symbol-opener?symbol=NAME&cwd=CWD`
- If followed by `()`, adds `&kind=Function` to the URL

**Requirements:**

- Install the [symbol-opener](https://github.com/mash/symbol-opener) VS Code/Cursor extension
- The extension uses LSP to resolve symbol definitions

**Example:**

```
$ echo "undefined: NewLinker" | osc8wrap --scheme=cursor
undefined: NewLinker  # "NewLinker" is now a clickable link
```

Disable with `--no-symbol-links` if you don't need this feature.

## Terminal support

See [OSC 8 adoption in terminal emulators](https://github.com/Alhadis/OSC8-Adoption/) for a list of supported terminals.

## License

MIT
