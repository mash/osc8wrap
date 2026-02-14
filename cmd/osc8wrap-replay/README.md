# osc8wrap-replay

Interactive replay tool for `osc8wrap --debug-writes` logs.

## Usage

```bash
go run ./cmd/osc8wrap-replay --file /tmp/osc8wrap-debug-osc8wrap-20260214-110857.log
```

Each press of Return advances one write chunk.

Replay preserves write order, but not original timing.

## Stream Modes

```bash
# Replay transformed output bytes (default)
go run ./cmd/osc8wrap-replay --stream=output /tmp/osc8wrap-debug-osc8wrap-20260214-110857.log

# Replay original input bytes
go run ./cmd/osc8wrap-replay --stream=input /tmp/osc8wrap-debug-osc8wrap-20260214-110857.log
```

## Creating Logs

```bash
# Path is printed to stderr
osc8wrap --debug-writes codex
```
