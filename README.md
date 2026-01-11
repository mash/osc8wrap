# osc8wrap

A CLI tool that wraps any command and converts file paths and URLs in output to clickable [OSC 8 hyperlinks](https://gist.github.com/egmontkob/eb114294efbcd5adb1944c9f3cb5feda).

## Installation

```bash
go install github.com/mash/osc8wrap@latest
```

## Usage

```bash
osc8wrap [options] <command> [args...]
<other command> | osc8wrap [options]
```

### Options

- `--scheme=NAME` - URL scheme for file links (default: `file`)

The scheme can also be set via the `OSC8WRAP_SCHEME` environment variable. The CLI flag takes precedence.

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
| HTTPS URL            | `https://example.com/docs` |

File paths are only linked if the file exists.

### Editor schemes

By default, file links use the `file://` scheme. To open files directly in your editor at the specific line, use an editor-specific scheme:

| Scheme   | URL format                          |
| -------- | ----------------------------------- |
| file     | `file://hostname/path`              |
| vscode   | `vscode://file/path:line:col`       |
| cursor   | `cursor://file/path:line:col`       |
| zed      | `zed://file/path:line:col`          |

Any scheme name is accepted and will be formatted as `{scheme}://file{path}:{line}:{col}`.

## Terminal support

See [OSC 8 adoption in terminal emulators](https://github.com/Alhadis/OSC8-Adoption/) for a list of supported terminals.

## License

MIT
