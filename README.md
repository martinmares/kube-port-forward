# kube-port-forward

A CLI wrapper around `kubectl port-forward` that automatically reconnects when the connection is lost.

## The Problem

`kubectl port-forward` occasionally drops with:

```
Forwarding from 127.0.0.1:9090 -> 9090
Handling connection for 9090
error: lost connection to pod
```

When this happens, you have to manually restart the command. This wrapper does it for you.

## Installation

Build from source (requires Go 1.21+):

```bash
go build -o kube-port-forward .
```

Optionally install globally:

```bash
sudo cp kube-port-forward /usr/local/bin/
```

## Usage

```
kube-port-forward [OPTIONS] -- <kubectl port-forward args...>
```

The `port-forward` subcommand is added automatically — do not include it in the args.

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `--retries` | `always` | Total number of attempts: positive integer or `always` for infinite |
| `--retry-interval` | `5` | Seconds to wait between reconnect attempts |
| `--log-level` | `info` | Log verbosity: `debug`, `info`, `warn`, `error` |

### Examples

Forward a pod port (reconnects automatically on failure):
```bash
kube-port-forward -- -n nac-app pod/nac-out-6c7985556d-h2cns 9090:9090
```

Forward two pods on different local ports:
```bash
kube-port-forward -- -n nac-app pod/nac-out-6c7985556d-h2cns 9090:9090
kube-port-forward -- -n nac-app pod/nac-out-6c7985556d-kqt8l 9091:9090
```

Forward via service:
```bash
kube-port-forward -- -n nac-app svc/nac-out 9090:9090
```

Limit to 5 attempts, wait 10 seconds between retries:
```bash
kube-port-forward --retries 5 --retry-interval 10 -- -n nac-app pod/nac-out-xxx 9090:9090
```

Show all kubectl output (debug level):
```bash
kube-port-forward --log-level debug -- -n nac-app pod/nac-out-xxx 9090:9090
```

## Log Output

Wrapper status messages are prefixed with log level (`INF`, `WRN`, `ERR`).
Lines originating from `kubectl` are prefixed with `[kubectl]`.

```
09:37:17 INF starting port-forward attempt=1/always cmd="kubectl port-forward -n nac-app pod/nac-out-xxx 9090:9090"
09:37:17 INF [kubectl] Forwarding from 127.0.0.1:9090 -> 9090
09:37:17 INF [kubectl] Forwarding from [::1]:9090 -> 9090
09:37:18 INF [kubectl] Handling connection for 9090
09:38:01 WRN lost connection to pod
09:38:01 INF reconnecting in=5s
09:38:06 INF starting port-forward attempt=2/always cmd="kubectl port-forward -n nac-app pod/nac-out-xxx 9090:9090"
09:38:06 INF [kubectl] Forwarding from 127.0.0.1:9090 -> 9090
```

## Notes

- The wrapper is intentionally limited to `kubectl port-forward`. Passing other subcommands will result in a `kubectl` error.
- Press `Ctrl+C` or send `SIGTERM` for a clean shutdown.
