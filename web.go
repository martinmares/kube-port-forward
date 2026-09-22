package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/mares/kube-port-forward/internal/diagnostic"
)

type webServer struct {
	manager    *forwardManager
	diagnostic *diagnosticManager
	client     *k8sClient
	cfg        *config
	logger     *slog.Logger
}

func newWebServer(manager *forwardManager, diagnosticManager *diagnosticManager, client *k8sClient, cfg *config, logger *slog.Logger) *webServer {
	return &webServer{manager: manager, diagnostic: diagnosticManager, client: client, cfg: cfg, logger: logger}
}

func (s *webServer) start(ctx context.Context) error {
	srv := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", s.cfg.webPort),
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	s.logger.Info("web UI started", "url", fmt.Sprintf("http://127.0.0.1:%d", s.cfg.webPort))

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *webServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/events", s.handleSSE)
	mux.HandleFunc("POST /api/diagnostic/start", s.handleDiagnosticStart)
	mux.HandleFunc("POST /api/diagnostic/stop", s.handleDiagnosticStop)
	mux.HandleFunc("POST /api/diagnostic/tunnels", s.handleDiagnosticTunnelCreate)
	mux.HandleFunc("DELETE /api/diagnostic/tunnels/{id}", s.handleDiagnosticTunnelDelete)
	static := http.StripPrefix("/static/", http.FileServer(http.FS(uiStaticFS)))
	mux.Handle("/static/", cacheStatic(static))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/static/css/app.css", "/static/js/app.js", "/static/js/theme.js":
			w.Header().Set("Cache-Control", "no-cache")
		default:
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

type stateResponse struct {
	Cluster    clusterInfo        `json:"cluster"`
	Resources  []resourceInfo     `json:"resources"`
	Pods       []podEntryJSON     `json:"pods"`
	Diagnostic diagnosticSnapshot `json:"diagnostic"`
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
		Cluster:    s.client.info,
		Resources:  resources,
		Pods:       pods,
		Diagnostic: s.diagnostic.snapshot(),
	}
}

type diagnosticStartRequest struct {
	Image            string   `json:"image"`
	PullPolicy       string   `json:"pullPolicy"`
	ImagePullSecrets []string `json:"imagePullSecrets"`
	TTL              string   `json:"ttl"`
	MaxConnections   int      `json:"maxConnections"`
	ConnectTimeout   string   `json:"connectTimeout"`
	IdleTimeout      string   `json:"idleTimeout"`
	PolicyYAML       string   `json:"policyYAML"`
}

type diagnosticTunnelRequest struct {
	Name          string `json:"name"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	LocalPort     int    `json:"localPort"`
	Mode          string `json:"mode"`
	TLSSkipVerify bool   `json:"tlsSkipVerify"`
}

func (s *webServer) handleDiagnosticStart(w http.ResponseWriter, r *http.Request) {
	if !s.allowMutation(w, r) {
		return
	}
	var request diagnosticStartRequest
	if !decodeRequestJSON(w, r, &request) {
		return
	}
	policy, err := diagnostic.ParsePolicyYAML(request.PolicyYAML)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	profile := diagnostic.Profile{
		Image: request.Image, PullPolicy: request.PullPolicy, ImagePullSecrets: request.ImagePullSecrets,
		TTL: request.TTL, MaxConnections: request.MaxConnections, ConnectTimeout: request.ConnectTimeout,
		IdleTimeout: request.IdleTimeout, Policy: policy,
	}
	if err := s.diagnostic.start(profile); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.diagnostic.snapshot())
}

func (s *webServer) handleDiagnosticStop(w http.ResponseWriter, r *http.Request) {
	if !s.allowMutation(w, r) {
		return
	}
	if err := s.diagnostic.stop(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.diagnostic.snapshot())
}

func (s *webServer) handleDiagnosticTunnelCreate(w http.ResponseWriter, r *http.Request) {
	if !s.allowMutation(w, r) {
		return
	}
	var request diagnosticTunnelRequest
	if !decodeRequestJSON(w, r, &request) {
		return
	}
	tunnel, err := s.diagnostic.addTunnel(request.Name, request.Host, request.Port, request.LocalPort, request.Mode, request.TLSSkipVerify)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, tunnel)
}

func (s *webServer) handleDiagnosticTunnelDelete(w http.ResponseWriter, r *http.Request) {
	if !s.allowMutation(w, r) {
		return
	}
	if err := s.diagnostic.removeTunnel(r.PathValue("id")); err != nil {
		writeAPIError(w, http.StatusNotFound, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *webServer) allowMutation(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Kube-Port-Forward") != "1" {
		writeAPIError(w, http.StatusForbidden, fmt.Errorf("missing same-origin request header"))
		return false
	}
	origin := r.Header.Get("Origin")
	if origin != "" {
		expected := fmt.Sprintf("http://127.0.0.1:%d", s.cfg.webPort)
		if origin != expected {
			writeAPIError(w, http.StatusForbidden, fmt.Errorf("request origin is not allowed"))
			return false
		}
	}
	return true
}

func decodeRequestJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, fmt.Errorf("Content-Type must be application/json"))
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeAPIError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON request: %w", err))
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeAPIError(w, http.StatusBadRequest, fmt.Errorf("request body must contain one JSON object"))
		return false
	}
	return true
}

func writeAPIError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *webServer) handleState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
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
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}
