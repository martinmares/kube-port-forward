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
	srv := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", s.cfg.webPort),
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
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
	static := http.StripPrefix("/static/", http.FileServer(http.FS(uiStaticFS)))
	mux.Handle("/static/", cacheStatic(static))
	return mux
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/static/css/app.css", "/static/js/app.js":
			w.Header().Set("Cache-Control", "no-cache")
		default:
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
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
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}
