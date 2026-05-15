# kube-port-forward

A Kubernetes port-forward manager with automatic reconnect and a live web UI.

## Features

- **Auto-reconnect** — restarts port-forward automatically when the connection drops
- **Service support** — resolves a Service to all backing pods and forwards each on a separate local port
- **Dynamic watch** — detects new or removed pods in real time; new pods get a new local port automatically
- **Wildcard ports** — `9091:*` expands to all `containerPort` values declared in the pod spec
- **Multiple resources** — forward several services or pods in a single command
- **Web UI** — live browser page with pod status, metadata, one-click open/copy, auto-refresh via SSE
- **Namespace autodetect** — reads default namespace from `~/.kube/config` if `-n` is omitted
- **No kubectl dependency** — uses the official `client-go` library directly

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
kube-port-forward [OPTIONS] -n <namespace> <pod|svc>/<name> <localPort>:<remotePort|*> [...]
```

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `-n` | kubeconfig default | Kubernetes namespace |
| `--retries` | `always` | Total reconnect attempts per pod: positive integer or `always` |
| `--retry-interval` | `5` | Seconds to wait between reconnect attempts |
| `--log-level` | `info` | Log verbosity: `debug`, `info`, `warn`, `error` |
| `--web-port` | `9999` | Local port for the web UI |
| `--no-open` | off | Disable automatic browser opening on start |

### Port mapping syntax

| Syntax | Meaning |
|--------|---------|
| `9091:9090` | Forward local `9091` to pod port `9090` (explicit) |
| `9091:*` | Forward starting from local `9091` to **all** `containerPort` values in the pod spec |

> **Important:** `localPort:remotePort` always tunnels directly to the **pod**, not through the
> Service. This means `remotePort` refers to what the pod actually listens on (`containerPort`),
> not the Service's `port` or `targetPort` fields.
>
> Example: if a Service defines `port: 9090 → targetPort: 8672`, using `9091:9090` will forward
> to pod port `9090` (the real pod port), **not** `8672`. Use `pod/<name>` or `svc/<name>` —
> the only difference is how pods are discovered.

### Examples

Forward a single pod to an explicit port:
```bash
kube-port-forward -n nac-app pod/nac-out-6c7985556d-h2cns 9090:9090
```

Forward all pods behind a service to an explicit remote port:
```bash
kube-port-forward -n nac-app svc/nac-out 9091:9090
```

Forward all pods, all container ports (wildcard):
```bash
kube-port-forward -n nac-app svc/nac-out 9091:*
# pod has containerPort 8672 and 9090 →
#   127.0.0.1:9091 → pod:8672
#   127.0.0.1:9092 → pod:9090
```

Forward multiple services in one command:
```bash
kube-port-forward -n nac-app svc/foo 9091:9090 svc/bar 9093:8080
```

Limit retries, custom interval, no browser:
```bash
kube-port-forward --retries 5 --retry-interval 10 --no-open -n nac-app svc/nac-out 9091:9090
```

## Web UI

On startup, a browser window opens at `http://127.0.0.1:9999` (configurable via `--web-port`).

The page shows:
- Cluster context, server URL, namespace, and target resource(s)
- Live list of forwarded pods with status, ready count, restart count (colour-coded), pod IP, node, and age
- **open ↗** — opens `http://127.0.0.1:<port>` in a new tab
- **copy** — copies the URL to clipboard
- Terminated pods remain visible (greyed out)
- Browser tab title shows `(forwarding/total) kube-port-forward`
- Favicon reflects overall health: green / yellow / red

The UI updates in real time via SSE — no page refresh needed.

## Health endpoint

```
GET http://127.0.0.1:9999/healthz
```

Returns JSON with status `ok` (HTTP 200) or `degraded` (HTTP 503):

```json
{"status":"ok","pods":[{"podName":"nac-out-xxx","localPort":9091,"status":"forwarding"}]}
```

## Port assignment

When forwarding a service with multiple pods, local ports are assigned sequentially and never recycled:

```
svc/nac-out  →  pod-abc  →  127.0.0.1:9091
             →  pod-def  →  127.0.0.1:9092
             →  pod-ghi  →  127.0.0.1:9093  (detected dynamically via Watch)
```

A terminated pod's port stays reserved so numbering remains stable across reconnects.

## Notes

- Press `Ctrl+C` or send `SIGTERM` for a clean shutdown.
- `--log-level debug` shows raw port-forward events per pod.
- Internal `client-go` log noise can be suppressed with `--log-level warn`.
