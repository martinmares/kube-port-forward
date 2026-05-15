package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

type webServer struct {
	manager *forwardManager
	client  *k8sClient
	cfg     *config
	logger  *slog.Logger
}

func newWebServer(manager *forwardManager, client *k8sClient, cfg *config, logger *slog.Logger) *webServer {
	return &webServer{manager: manager, client: client, cfg: cfg, logger: logger}
}

func (s *webServer) start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/events", s.handleSSE)

	srv := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", s.cfg.webPort),
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()

	s.logger.Info("web UI started", "url", fmt.Sprintf("http://127.0.0.1:%d", s.cfg.webPort))

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

type stateResponse struct {
	Cluster   clusterInfo    `json:"cluster"`
	Resources []resourceInfo `json:"resources"`
	Pods      []podEntryJSON `json:"pods"`
}

type resourceInfo struct {
	Namespace string `json:"namespace"`
	Resource  string `json:"resource"`
}

type podEntryJSON struct {
	PodName    string `json:"podName"`
	LocalPort  int    `json:"localPort"`
	RemotePort int    `json:"remotePort"`
	Status     string `json:"status"`
	Attempt    int    `json:"attempt"`
	URL        string `json:"url"`
	NodeName   string `json:"nodeName"`
	PodIP      string `json:"podIP"`
	Ready      string `json:"ready"`
	Restarts   int32  `json:"restarts"`
	StartTime  string `json:"startTime"` // RFC3339 for JS age calculation
	Resource   string `json:"resource"`  // which resource this pod belongs to
}

type healthResponse struct {
	Status string         `json:"status"`
	Pods   []podEntryJSON `json:"pods"`
}

func (s *webServer) buildState() stateResponse {
	snaps := s.manager.snapshot()
	pods := make([]podEntryJSON, len(snaps))
	for i, e := range snaps {
		pods[i] = podEntryJSON{
			PodName:    e.podName,
			LocalPort:  e.localPort,
			RemotePort: e.remotePort,
			Status:     e.status.String(),
			Attempt:    e.attempt,
			URL:        fmt.Sprintf("http://127.0.0.1:%d", e.localPort),
			NodeName:   e.meta.nodeName,
			PodIP:      e.meta.podIP,
			Ready:      e.meta.ready,
			Restarts:   e.meta.restarts,
			StartTime:  e.meta.startTime,
			Resource:   e.resource,
		}
	}

	resources := make([]resourceInfo, len(s.cfg.targets))
	for i, t := range s.cfg.targets {
		resources[i] = resourceInfo{
			Namespace: t.namespace,
			Resource:  t.resourceStr(),
		}
	}

	return stateResponse{
		Cluster:   s.client.info,
		Resources: resources,
		Pods:      pods,
	}
}

func (s *webServer) handleState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.buildState())
}

func (s *webServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	snaps := s.manager.snapshot()
	pods := make([]podEntryJSON, len(snaps))
	allOK := true
	for i, e := range snaps {
		pods[i] = podEntryJSON{
			PodName:   e.podName,
			LocalPort: e.localPort,
			Status:    e.status.String(),
		}
		if e.status != statusForwarding {
			allOK = false
		}
	}
	status := "ok"
	if !allOK {
		status = "degraded"
	}
	w.Header().Set("Content-Type", "application/json")
	if !allOK {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(healthResponse{Status: status, Pods: pods})
}

func (s *webServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	sendState := func() {
		data, _ := json.Marshal(s.buildState())
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	sendState()

	for {
		notify := s.manager.subscribe()
		select {
		case <-r.Context().Done():
			return
		case <-notify:
			sendState()
		}
	}
}

func (s *webServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, indexHTML)
}

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>kube-port-forward</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: 'SF Mono', 'Fira Code', monospace; background: #0f1117; color: #e2e8f0; min-height: 100vh; padding: 24px; }
  h1 { font-size: 1.1rem; font-weight: 600; color: #94a3b8; letter-spacing: 0.05em; margin-bottom: 20px; }
  .cluster-info { background: #1e2130; border: 1px solid #2d3148; border-radius: 8px; padding: 16px 20px; margin-bottom: 24px; display: flex; flex-direction: column; gap: 8px; }
  .info-item { display: flex; align-items: baseline; gap: 12px; }
  .info-label { font-size: 0.65rem; color: #64748b; text-transform: uppercase; letter-spacing: 0.08em; min-width: 80px; flex-shrink: 0; }
  .info-value { font-size: 0.85rem; color: #cbd5e1; word-break: break-all; }
  .info-value.multi { display: flex; flex-direction: column; gap: 2px; }
  .pods-header { font-size: 0.7rem; color: #475569; text-transform: uppercase; letter-spacing: 0.08em; margin-bottom: 10px; }
  .pod-list { display: flex; flex-direction: column; gap: 8px; }
  .pod-card { background: #1e2130; border: 1px solid #2d3148; border-radius: 8px; padding: 14px 18px; display: flex; align-items: center; gap: 16px; transition: border-color 0.2s; }
  .pod-card:hover { border-color: #3d4468; }
  .pod-card.terminated { opacity: 0.45; }
  .status-dot { font-size: 1.1rem; flex-shrink: 0; }
  .pod-info { flex: 1; min-width: 0; }
  .pod-name { font-size: 0.8rem; color: #94a3b8; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; margin-bottom: 4px; }
  .pod-port { font-size: 0.75rem; color: #475569; margin-bottom: 5px; }
  .pod-meta { display: flex; flex-wrap: wrap; gap: 6px; }
  .meta-tag { font-size: 0.68rem; color: #64748b; background: #151822; border: 1px solid #2d3148; border-radius: 4px; padding: 1px 7px; white-space: nowrap; }
  .meta-tag span { margin-left: 3px; }
  .meta-tag span.ok { color: #94a3b8; }
  .meta-tag span.warn { color: #facc15; }
  .meta-tag span.crit { color: #f87171; }
  .pod-status { font-size: 0.7rem; padding: 2px 8px; border-radius: 9999px; border: 1px solid; flex-shrink: 0; }
  .status-forwarding { color: #4ade80; border-color: #166534; background: #052e16; }
  .status-connecting, .status-reconnecting { color: #facc15; border-color: #713f12; background: #1c1103; }
  .status-terminated { color: #f87171; border-color: #7f1d1d; background: #1c0505; }
  .actions { display: flex; gap: 6px; flex-shrink: 0; }
  .btn { font-family: inherit; font-size: 0.7rem; padding: 4px 10px; border-radius: 5px; border: 1px solid #2d3148; background: #262b3d; color: #94a3b8; cursor: pointer; transition: all 0.15s; text-decoration: none; display: inline-flex; align-items: center; }
  .btn:hover { background: #323859; border-color: #4a5180; color: #e2e8f0; }
  .btn.copied { color: #4ade80; border-color: #166534; }
  .empty { color: #475569; font-size: 0.85rem; padding: 32px 0; text-align: center; }
  .dot-pulse { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: #4ade80; animation: pulse 1.5s ease-in-out infinite; margin-right: 8px; vertical-align: middle; }
  @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.3; } }
</style>
</head>
<body>
<h1><span class="dot-pulse"></span>kube-port-forward</h1>
<div class="cluster-info" id="cluster-info"></div>
<div class="pods-header">Active forwards</div>
<div class="pod-list" id="pod-list"><div class="empty">Waiting for pods…</div></div>

<script>
let _state = null;

function render(state) {
  _state = state;

  // cluster info
  const ci = document.getElementById('cluster-info');
  const resourceValues = (state.resources || []).map(r => r.namespace + ' / ' + r.resource);
  ci.innerHTML = [
    ['Context',   state.cluster.contextName],
    ['Cluster',   state.cluster.clusterName || '—'],
    ['Server',    state.cluster.serverURL],
    ['Namespace', [...new Set((state.resources||[]).map(r=>r.namespace))].join(', ') || state.cluster.namespace],
    ['Resource',  resourceValues],
  ].map(([label, value]) => {
    const multi = Array.isArray(value);
    const inner = multi
      ? '<span class="info-value multi">' + value.map(v => esc(v)).join('') + '</span>'
      : '<span class="info-value">' + esc(value) + '</span>';
    return '<div class="info-item"><span class="info-label">' + label + '</span>' + inner + '</div>';
  }).join('');

  // pod list
  const pl = document.getElementById('pod-list');
  if (!state.pods || state.pods.length === 0) {
    pl.innerHTML = '<div class="empty">No pods found yet…</div>';
    updateTitle(0, 0);
    return;
  }

  const forwarding = state.pods.filter(p => p.status === 'forwarding').length;
  updateTitle(forwarding, state.pods.length);

  pl.innerHTML = state.pods.map(pod => {
    const terminated = pod.status === 'terminated';
    const statusClass = 'status-' + pod.status;
    const icon = {forwarding:'●', connecting:'◌', reconnecting:'◌', terminated:'✕'}[pod.status] || '?';
    const actions = terminated ? '' : [
      '<a class="btn" href="' + esc(pod.url) + '" target="_blank" title="Open in new tab">open ↗</a>',
      '<button class="btn" onclick="copyURL(this, \'' + esc(pod.url) + '\')" title="Copy URL">copy</button>',
    ].join('');

    const restartsClass = pod.restarts === 0 ? 'ok' : pod.restarts < 5 ? 'warn' : 'crit';

    return '<div class="pod-card' + (terminated ? ' terminated' : '') + '">' +
      '<span class="status-dot" style="color:' + statusColor(pod.status) + '">' + icon + '</span>' +
      '<div class="pod-info">' +
        '<div class="pod-name" title="' + esc(pod.podName) + '">' + esc(pod.podName) + '</div>' +
        '<div class="pod-port">127.0.0.1:<strong>' + pod.localPort + '</strong> → ' + pod.remotePort +
          (pod.attempt > 1 ? ' &nbsp;·&nbsp; attempt ' + pod.attempt : '') + '</div>' +
        '<div class="pod-meta">' +
          tag('ready', pod.ready, 'ok') +
          '<span class="meta-tag">restarts<span class="' + restartsClass + '">' + pod.restarts + '</span></span>' +
          tag('ip', pod.podIP || '—', 'ok') +
          tag('node', pod.nodeName || '—', 'ok') +
          (pod.startTime ? '<span class="meta-tag">age<span class="ok" data-start="' + esc(pod.startTime) + '">…</span></span>' : '') +
        '</div>' +
      '</div>' +
      '<span class="pod-status ' + statusClass + '">' + pod.status + '</span>' +
      '<div class="actions">' + actions + '</div>' +
    '</div>';
  }).join('');

  refreshAges();
}

function updateTitle(forwarding, total) {
  document.title = total > 0
    ? '(' + forwarding + '/' + total + ') kube-port-forward'
    : 'kube-port-forward';
  // update favicon color based on health
  setFavicon(forwarding === total && total > 0 ? '#4ade80' : forwarding > 0 ? '#facc15' : '#f87171');
}

function setFavicon(color) {
  const svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="7" fill="' + color + '"/></svg>';
  const url = 'data:image/svg+xml,' + encodeURIComponent(svg);
  let link = document.querySelector('link[rel="icon"]');
  if (!link) { link = document.createElement('link'); link.rel = 'icon'; document.head.appendChild(link); }
  link.href = url;
}

function refreshAges() {
  document.querySelectorAll('[data-start]').forEach(el => {
    el.textContent = age(el.dataset.start);
  });
}

function age(iso) {
  const secs = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
  if (secs < 60) return secs + 's';
  const mins = Math.floor(secs / 60);
  if (mins < 60) return mins + 'm';
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return hrs + 'h ' + (mins % 60) + 'm';
  const days = Math.floor(hrs / 24);
  return days + 'd ' + (hrs % 24) + 'h';
}

function tag(label, value, cls) {
  return '<span class="meta-tag">' + label + '<span class="' + (cls||'ok') + '">' + esc(String(value)) + '</span></span>';
}

function statusColor(s) {
  return {forwarding:'#4ade80', connecting:'#facc15', reconnecting:'#facc15', terminated:'#f87171'}[s] || '#9ca3af';
}

function esc(s) {
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

function copyURL(btn, url) {
  navigator.clipboard.writeText(url).then(() => {
    btn.textContent = 'copied!';
    btn.classList.add('copied');
    setTimeout(() => { btn.textContent = 'copy'; btn.classList.remove('copied'); }, 1500);
  });
}

// refresh age counters every 30s without full re-render
setInterval(refreshAges, 30000);

const es = new EventSource('/events');
es.onmessage = e => render(JSON.parse(e.data));
es.onerror = () => console.warn('SSE connection lost, retrying…');
</script>
</body>
</html>`
