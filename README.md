# osc8wrap

A CLI tool that wraps any command and converts file paths and URLs in output to clickable [OSC 8 hyperlinks](https://gist.github.com/egmontkob/eb114294efbcd5adb1944c9f3cb5feda).

## Installation

```bash
go install github.com/mash/osc8wrap@latest
```

## Usage

```bash
osc8wrap <command> [args...]
```

### Examples

```bash
# Make file paths in build errors clickable
osc8wrap go build ./...

# Make grep results clickable
osc8wrap grep -rn "TODO" .

# Make Claude Code output clickable
osc8wrap claude
```

## What it does

- Detects file paths (absolute and relative) in command output
- Detects `https://` URLs
- Converts them to OSC 8 hyperlinks that work in supported terminals
- Runs commands through a PTY, so colors and interactive programs work

### Supported patterns

| Pattern              | Example                    |
| -------------------- | -------------------------- |
| Absolute path        | `/path/to/file.go`         |
| With line number     | `/path/to/file.go:42`      |
| With line and column | `/path/to/file.go:42:10`   |
| Relative path        | `./src/main.go:10`         |
| HTTPS URL            | `https://example.com/docs` |

File paths are only linked if the file exists.

## Terminal support

See [OSC 8 adoption in terminal emulators](https://github.com/Alhadis/OSC8-Adoption/) for a list of supported terminals.

## License

MIT
