# kube-port-forward

**English** | [Česky](README.cs.md)

A Kubernetes port-forward manager with automatic reconnect and a live web UI.

## Features

- **Auto-reconnect** — restarts a port-forward when its connection drops
- **Pod, Service, and Deployment support** — targets one pod or discovers pods through selectors
- **Namespace-wide Service discovery** — `svc/*` forwards all usable Services in a namespace
- **Dynamic pod watch** — detects added and removed pods, including Deployment rollouts
- **Wildcard ports** — `9091:*` expands to every `containerPort` declared by the first running selected pod
- **Collision-free allocation** — reserves local ports globally across resources, replicas, and remote ports
- **Duplicate suppression** — shares one forward when multiple resources select the same pod and remote port
- **Multiple resources** — forwards several targets in one process
- **Web UI** — shows live status and metadata and provides open/copy actions
- **Diagnostic gateway** — exposes allowlisted cluster-only TCP, HTTP, or HTTPS endpoints through a short-lived proxy pod
- **Namespace autodetection** — reads the current-context namespace from kubeconfig
- **No kubectl dependency** — uses the official `client-go` library directly

## Requirements and build

Building requires the Go version declared in [`go.mod`](go.mod) (currently Go 1.26.3).

With [just](https://just.systems/) installed:

| Command | Target | Output |
|---------|--------|--------|
| `just build` | Current platform | `dist/kube-port-forward` |
| `just build-macos` | macOS / Apple Silicon | `dist/kube-port-forward-darwin-arm64` |
| `just build-linux` | Linux / AMD64 | `dist/kube-port-forward-linux-amd64` |
| `just build-win` | Windows / AMD64 | `dist/kube-port-forward-windows-amd64.exe` |
| `just build-proxy` | Current platform | `dist/kube-port-forward-proxy` |
| `just build-proxy-linux` | Linux / AMD64 | `dist/kube-port-forward-proxy-linux-amd64` |

Alternatively, build directly with Go:

```bash
mkdir -p dist
go build -o dist/kube-port-forward .
```

Optionally install the current-platform build globally:

```bash
sudo cp dist/kube-port-forward /usr/local/bin/
```

## Usage

```text
kube-port-forward [OPTIONS] -n <namespace> [<pod|svc|deploy>/<name> <localPort>:<remotePort|*> ...]
```

### Supported targets

| Target | Pod discovery |
|--------|---------------|
| `pod/<name>` | Uses exactly one named pod |
| `svc/<name>` | Uses all running pods matching the Service selector |
| `deploy/<name>` | Uses all running pods matching the Deployment selector |
| `svc/*` | Loads all Services with pod selectors in the namespace |

`svc/*` loads the Services that exist at startup. Their selected pods are then watched
dynamically. Services created later require restarting the application.

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `-n`, `--namespace` | kubeconfig default | Kubernetes namespace |
| `--retries` | `always` | Maximum connection attempts per pod: positive integer or `always` |
| `--retry-interval` | `5` | Seconds between reconnect attempts |
| `--log-level` | `info` | Log verbosity: `debug`, `info`, `warn`, `error` |
| `--web-port` | `9999` | Local port for the web UI |
| `--no-open` | off | Disables automatic browser opening |
| `--insecure` | off | Skips Kubernetes API server TLS certificate verification |
| `--diagnostic-config` | off | Enables the diagnostic gateway using a local YAML profile |

> **Warning:** `--insecure` disables verification of the API server's TLS certificate. Use it
> only for clusters you trust.

### Port mapping

| Syntax | Meaning |
|--------|---------|
| `9091:9090` | Starts at local port `9091` and forwards to pod port `9090` |
| `9091:*` | Starts at local port `9091` and forwards every `containerPort` from the first running selected pod |

Local ports are allocated globally. Allocation starts at the requested local port and moves
upward to the first port not already reserved by this process. Ports remain reserved after a pod
terminates, keeping assignments stable during reconnects and rollouts.

> **Important:** forwarding always connects directly to a **pod**, even when discovery uses a
> Service. The remote port is therefore a pod port, not the Service's `port`.
>
> For example, if a Service defines `port: 9090 → targetPort: 8672`, mapping `9091:9090`
> connects to pod port `9090`, not `8672`.

### Examples

Forward one pod to an explicit port:

```bash
kube-port-forward -n nac-app pod/nac-out-6c7985556d-h2cns 9090:9090
```

Forward all pods selected by a Service:

```bash
kube-port-forward -n nac-app svc/nac-out 9091:9090
```

Forward every declared container port on all pods selected by a Service:

```bash
kube-port-forward -n nac-app svc/nac-out 9091:*
```

Forward every declared container port on all pods in a Deployment:

```bash
kube-port-forward -n nac-app deploy/nac-out 9091:*
```

Discover all usable Services in a namespace:

```bash
kube-port-forward -n nac-app 'svc/*' 10000:*
```

The quotes prevent the shell from expanding `*`. Services without pod selectors are skipped.
With wildcard ports, Services without running pods or declared `containerPort` values are also
logged and skipped.

Forward multiple resources:

```bash
kube-port-forward -n nac-app svc/foo 9091:9090 deploy/bar 9093:8080
```

Set retry behavior and disable automatic browser opening:

```bash
kube-port-forward --retries 5 --retry-interval 10 --no-open \
  -n nac-app svc/nac-out 9091:9090
```

Connect without verifying the API server certificate:

```bash
kube-port-forward --insecure \
  -n nac-app deploy/nac-out 9091:*
```

## Web UI

At startup, the application serves `http://127.0.0.1:9999` and opens it in a browser unless
`--no-open` is set. The port is configurable with `--web-port`.

The page shows:

- cluster context, server URL, namespace, and configured targets
- source resource for every forward
- pod status, ready count, restart count, IP, node, and age
- local-to-remote port mapping
- **open ↗** and **copy** actions
- summary cards for forwarding, connecting, reconnecting, and historical forwards
- search and filters by status or source resource
- optional terminated-pod history, retained as dimmed entries
- responsive table/card layout and light, dark, or automatic color theme
- forwarding totals in the browser title and health state in the favicon

The UI updates through server-sent events (SSE), without page refreshes, and reports the
current connection state in the footer.

## Diagnostic gateway

The diagnostic gateway provides access to a hostname and port that is reachable only from the
cluster. The application creates a short-lived proxy Pod, immutable policy ConfigMap, and
ephemeral authentication Secret, opens a private Kubernetes port-forward to it, and exposes
selected targets on `127.0.0.1`.

Build and publish the proxy image to a registry available to the cluster:

```bash
just docker-build-proxy registry.example.com/tools/kube-port-forward-proxy:latest
docker push registry.example.com/tools/kube-port-forward-proxy:latest
```

Copy [`diagnostic.example.yaml`](diagnostic.example.yaml), set the image and allow/deny rules,
then start the application:

```bash
kube-port-forward -n nac-app \
  --diagnostic-config ~/.config/kube-port-forward/ocp-prod.yaml
```

The profile has no schema-version field. Unknown fields are rejected to catch configuration
mistakes. Deny rules override allow rules, and the default example denies all unmatched targets.
Hostnames are resolved inside the cluster; every resolved IP is checked before the proxy connects
directly to an allowed address.

The UI can edit a working copy of the profile, start or stop the diagnostic Pod, test target
connectivity, and create TCP, HTTP, or HTTPS local endpoints. Changes made in the UI do not overwrite the
local YAML file.

TCP mode provides an unmodified byte stream and keeps TLS end-to-end in the local client. HTTP mode
uses plaintext HTTP upstream. HTTPS mode accepts local HTTP on `127.0.0.1`, connects to the target
over TLS, and automatically sets the target HTTP `Host` and TLS SNI. Upstream certificates are
verified by default; **Skip upstream TLS certificate verification (insecure)** is an explicit
per-tunnel override for trusted internal endpoints. Authentication headers supplied by the local
client are forwarded per request and are never stored or logged by the application.

### End-to-end example: OpenShift Thanos API

This example exposes an OpenShift Thanos/Prometheus API route from the `nac-app` namespace. It
assumes Docker is already authenticated to `quay.in.cetin.sk`.

1. Build the proxy for the cluster architecture and push it to the internal registry:

   ```bash
   docker buildx build \
     --platform linux/amd64 \
     -f Dockerfile.proxy \
     -t quay.in.cetin.sk/nac_prod/kube-port-forward-proxy:latest \
     --push .
   ```

2. Create `.tmp/diagnostic-thanos.yaml` without a schema-version field:

   ```yaml
   image: quay.in.cetin.sk/nac_prod/kube-port-forward-proxy:latest
   pullPolicy: Always
   imagePullSecrets:
     - tsm-oc-pull-secret
   ttl: 1h
   maxConnections: 16
   connectTimeout: 10s
   idleTimeout: 15m

   policy:
     defaultAction: deny
     allow:
       - hosts:
           - thanos-querier-openshift-monitoring.apps.ocp2.in.cetin.sk
         ports:
           - "443"
   ```

3. Start the application. `--insecure` is needed only when the Kubernetes API certificate cannot
   be verified locally:

   ```bash
   ./dist/kube-port-forward \
     --insecure \
     -n nac-app \
     --diagnostic-config .tmp/diagnostic-thanos.yaml
   ```

4. Open `http://127.0.0.1:9999`, click **Start diagnostic pod**, and wait for **Ready**.

5. Create an endpoint in the UI with these values:

   | Field | Value |
   |-------|-------|
   | Name | `Thanos API` |
   | Cluster hostname or IP | `thanos-querier-openshift-monitoring.apps.ocp2.in.cetin.sk` |
   | Target port | `443` |
   | Local port | `18443` |
   | Mode | `HTTPS` |
   | Skip upstream TLS certificate verification | enabled for this internally signed route |

6. Store the bearer token locally and restrict its permissions:

   ```bash
   chmod 600 .tmp/token.txt
   ```

7. Query the Prometheus API through the local HTTP endpoint. The HTTPS tunnel sets the upstream
   `Host` and TLS SNI, while its explicit checkbox handles this example route's internally signed
   certificate:

   ```bash
   curl -s \
     -H "Authorization: Bearer $(<.tmp/token.txt)" \
     'http://127.0.0.1:18443/api/v1/query?query=up' \
     | jq
   ```

8. Download metric names and find metrics beginning with `nac_`:

   ```bash
   curl -s \
     -H "Authorization: Bearer $(<.tmp/token.txt)" \
     'http://127.0.0.1:18443/api/v1/label/__name__/values' \
     | jq -r '.data[]' | sort > .tmp/metrics.txt

   grep '^nac_' .tmp/metrics.txt
   ```

9. List label names or inspect all label sets belonging to `nac_` metrics:

   ```bash
   curl -s \
     -H "Authorization: Bearer $(<.tmp/token.txt)" \
     'http://127.0.0.1:18443/api/v1/labels' \
     | jq -r '.data[]' | sort

   curl -sG \
     -H "Authorization: Bearer $(<.tmp/token.txt)" \
     --data-urlencode 'match[]={__name__=~"nac_.*"}' \
     'http://127.0.0.1:18443/api/v1/series' \
     | jq '.data'
   ```

10. Click **Stop gateway** when finished. The UI waits until the Pod, ConfigMap, and Secret have
    been removed.

The Kubernetes identity used by the application needs `get` for Pods plus `create`, `get`,
`update`, and `delete` for Pods, ConfigMaps, and Secrets, and permission to create `pods/portforward`
requests in the selected namespace. The proxy Pod does not mount a service-account token, runs
as non-root with a read-only filesystem, drops all capabilities, and is automatically terminated
after the configured TTL.

## Health endpoint

```http
GET http://127.0.0.1:9999/healthz
```

The endpoint returns `ok` with HTTP 200 or `degraded` with HTTP 503:

```json
{"status":"ok","pods":[{"podName":"nac-out-xxx","localPort":9091,"status":"forwarding"}]}
```

## Port assignment example

For two pods declaring ports `8080` and `9090`, starting at local port `10000` produces
collision-free assignments:

```text
pod-a  127.0.0.1:10000 → 8080
pod-a  127.0.0.1:10001 → 9090
pod-b  127.0.0.1:10002 → 8080
pod-b  127.0.0.1:10003 → 9090
```

New pods continue at the next unreserved local port. Assignments are not recycled during the
process lifetime.

## Notes

- Press `Ctrl+C` or send `SIGTERM` for a clean shutdown.
- `--log-level debug` shows detailed discovery and forwarding events.
- `--log-level warn` suppresses most internal `client-go` log noise.
