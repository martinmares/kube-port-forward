package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

type podStatus int

const (
	statusConnecting podStatus = iota
	statusForwarding
	statusReconnecting
	statusTerminated
)

func (s podStatus) String() string {
	switch s {
	case statusConnecting:
		return "connecting"
	case statusForwarding:
		return "forwarding"
	case statusReconnecting:
		return "reconnecting"
	case statusTerminated:
		return "terminated"
	default:
		return "unknown"
	}
}

type podMeta struct {
	nodeName  string
	podIP     string
	ready     string // e.g. "2/2"
	restarts  int32
	startTime string // RFC3339
}

type podEntry struct {
	mu         sync.RWMutex
	podName    string
	namespace  string
	localPort  int
	remotePort int
	status     podStatus
	attempt    int
	meta       podMeta
	resource   string // e.g. "svc/nac-out"
}

type podSnapshot struct {
	podName    string
	namespace  string
	localPort  int
	remotePort int
	status     podStatus
	attempt    int
	meta       podMeta
	resource   string
}

func (e *podEntry) getStatus() podStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.status
}

func (e *podEntry) setStatus(s podStatus) {
	e.mu.Lock()
	e.status = s
	e.mu.Unlock()
}

// forwardManager manages all active port-forwards.
type forwardManager struct {
	mu           sync.RWMutex
	entries      []*podEntry // append-only, terminated entries kept for UI
	nextPorts    map[int]int // target index → next available local port
	cfg          *config
	client       *k8sClient
	logger       *slog.Logger
	notify       chan struct{} // closed+recreated on state change to notify SSE clients
	notifyMu     sync.Mutex
}

func newForwardManager(cfg *config, client *k8sClient, logger *slog.Logger) *forwardManager {
	nextPorts := make(map[int]int, len(cfg.targets))
	for i, t := range cfg.targets {
		nextPorts[i] = t.localPort
	}
	return &forwardManager{
		cfg:       cfg,
		client:    client,
		logger:    logger,
		nextPorts: nextPorts,
		notify:    make(chan struct{}),
	}
}

func (m *forwardManager) notifyClients() {
	m.notifyMu.Lock()
	old := m.notify
	m.notify = make(chan struct{})
	m.notifyMu.Unlock()
	close(old)
}

func (m *forwardManager) subscribe() <-chan struct{} {
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	return m.notify
}

// addPod starts a port-forward goroutine for a pod if not already tracked.
func (m *forwardManager) addPod(ctx context.Context, pod *corev1.Pod, targetIdx int) {
	t := m.cfg.targets[targetIdx]
	m.mu.Lock()
	for _, e := range m.entries {
		if e.podName == pod.Name && e.status != statusTerminated {
			m.mu.Unlock()
			return
		}
	}
	localPort := m.nextPorts[targetIdx]
	m.nextPorts[targetIdx]++
	entry := &podEntry{
		podName:    pod.Name,
		namespace:  pod.Namespace,
		localPort:  localPort,
		remotePort: t.remotePort,
		status:     statusConnecting,
		meta:       extractMeta(pod),
		resource:   t.resourceStr(),
	}
	m.entries = append(m.entries, entry)
	m.mu.Unlock()

	m.logger.Info("adding pod", "pod", pod.Name, "local_port", localPort, "resource", t.resourceStr())
	m.notifyClients()

	go m.runForward(ctx, entry)
}

// removePod marks a pod as terminated and stops its forward.
func (m *forwardManager) removePod(podName string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, e := range m.entries {
		if e.podName == podName {
			e.setStatus(statusTerminated)
			m.logger.Info("pod terminated", "pod", podName, "local_port", e.localPort)
			m.notifyClients()
			return
		}
	}
}

func (m *forwardManager) snapshot() []podSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]podSnapshot, len(m.entries))
	for i, e := range m.entries {
		e.mu.RLock()
		out[i] = podSnapshot{
			podName:    e.podName,
			namespace:  e.namespace,
			localPort:  e.localPort,
			remotePort: e.remotePort,
			status:     e.status,
			attempt:    e.attempt,
			meta:       e.meta,
			resource:   e.resource,
		}
		e.mu.RUnlock()
	}
	return out
}

func (m *forwardManager) runForward(ctx context.Context, entry *podEntry) {
	for {
		if entry.getStatus() == statusTerminated {
			return
		}

		entry.mu.Lock()
		entry.attempt++
		attempt := entry.attempt
		entry.status = statusConnecting
		entry.mu.Unlock()
		m.notifyClients()

		maxLabel := "always"
		if m.cfg.retries != retriesAlways {
			maxLabel = fmt.Sprintf("%d", m.cfg.retries)
		}
		m.logger.Info("connecting",
			"pod", entry.podName,
			"local_port", entry.localPort,
			"attempt", fmt.Sprintf("%d/%s", attempt, maxLabel),
		)

		err := m.doForward(ctx, entry)

		if ctx.Err() != nil {
			entry.setStatus(statusTerminated)
			m.notifyClients()
			return
		}

		if entry.getStatus() == statusTerminated {
			return
		}

		if err != nil {
			m.logger.Warn("forward lost", "pod", entry.podName, "err", err)
		} else {
			m.logger.Warn("forward ended", "pod", entry.podName)
		}

		if m.cfg.retries != retriesAlways && attempt >= m.cfg.retries {
			m.logger.Error("max attempts reached", "pod", entry.podName, "attempts", attempt)
			entry.setStatus(statusTerminated)
			m.notifyClients()
			return
		}

		entry.setStatus(statusReconnecting)
		m.notifyClients()
		m.logger.Info("reconnecting", "pod", entry.podName, "in", m.cfg.retryInterval)

		select {
		case <-ctx.Done():
			entry.setStatus(statusTerminated)
			m.notifyClients()
			return
		case <-time.After(m.cfg.retryInterval):
		}
	}
}

func (m *forwardManager) doForward(ctx context.Context, entry *podEntry) error {
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", entry.namespace, entry.podName)
	hostURL, err := url.Parse(m.client.config.Host)
	if err != nil {
		return err
	}
	hostURL.Path = path

	transport, upgrader, err := spdy.RoundTripperFor(m.client.config)
	if err != nil {
		return err
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, hostURL)

	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	errCh := make(chan error, 1)

	ports := []string{fmt.Sprintf("%d:%d", entry.localPort, entry.remotePort)}

	fw, err := portforward.New(dialer, ports, stopCh, readyCh, nil, nil)
	if err != nil {
		return err
	}

	go func() {
		errCh <- fw.ForwardPorts()
	}()

	select {
	case <-readyCh:
		entry.setStatus(statusForwarding)
		m.notifyClients()
		m.logger.Info("[kubectl] Forwarding from",
			"msg", fmt.Sprintf("127.0.0.1:%d -> %d", entry.localPort, entry.remotePort),
			"pod", entry.podName,
		)
	case err := <-errCh:
		return err
	case <-ctx.Done():
		close(stopCh)
		return ctx.Err()
	}

	// wait until context cancelled, pod terminated, or forward dies
	select {
	case <-ctx.Done():
		close(stopCh)
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func extractMeta(pod *corev1.Pod) podMeta {
	total := len(pod.Spec.Containers)
	ready := 0
	var restarts int32
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
		restarts += cs.RestartCount
	}

	startTime := ""
	if pod.Status.StartTime != nil {
		startTime = pod.Status.StartTime.UTC().Format("2006-01-02 15:04:05")
	}

	return podMeta{
		nodeName:  pod.Spec.NodeName,
		podIP:     pod.Status.PodIP,
		ready:     fmt.Sprintf("%d/%d", ready, total),
		restarts:  restarts,
		startTime: startTime,
	}
}

// watchAndSync starts watchers for all configured targets.
func (m *forwardManager) watchAndSync(ctx context.Context) error {
	errCh := make(chan error, len(m.cfg.targets))
	for i, t := range m.cfg.targets {
		go func(idx int, t target) {
			errCh <- m.watchTarget(ctx, idx, t)
		}(i, t)
	}
	// wait for first error or context cancel
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return nil
	}
}

func (m *forwardManager) watchTarget(ctx context.Context, idx int, t target) error {
	// wildcard port: expand into one sub-target per service port
	if t.remotePort == 0 {
		ports, err := m.client.resolveServicePorts(ctx, t.namespace, t.resourceName)
		if err != nil {
			return err
		}
		if len(ports) == 0 {
			return fmt.Errorf("service %q has no ports", t.resourceName)
		}
		m.logger.Info("expanding wildcard ports", "svc", t.resourceName, "ports", ports)

		// register sub-targets in forwardManager so they get their own nextPort slots
		m.mu.Lock()
		baseIdx := len(m.cfg.targets)
		for i, remotePort := range ports {
			sub := target{
				namespace:    t.namespace,
				resourceKind: t.resourceKind,
				resourceName: t.resourceName,
				localPort:    t.localPort + i,
				remotePort:   remotePort,
			}
			m.cfg.targets = append(m.cfg.targets, sub)
			m.nextPorts[baseIdx+i] = sub.localPort
		}
		m.mu.Unlock()

		errCh := make(chan error, len(ports))
		for i := range ports {
			go func(subIdx int) {
				errCh <- m.watchTarget(ctx, subIdx, m.cfg.targets[subIdx])
			}(baseIdx + i)
		}
		select {
		case err := <-errCh:
			return err
		case <-ctx.Done():
			return nil
		}
	}

	if t.resourceKind == resourcePod {
		pod, err := m.client.getPod(ctx, t.namespace, t.resourceName, m.logger)
		if err != nil {
			return err
		}
		m.addPod(ctx, pod, idx)
		<-ctx.Done()
		return nil
	}

	// service: resolve selector and watch pods
	selector, err := m.client.resolveService(ctx, t.namespace, t.resourceName)
	if err != nil {
		return err
	}
	m.logger.Info("resolved service", "svc", t.resourceName, "selector", selector.String())

	pods, err := m.client.listPods(ctx, t.namespace, selector)
	if err != nil {
		return err
	}
	m.logger.Info("found pods", "count", len(pods), "svc", t.resourceName)
	for i := range pods {
		m.addPod(ctx, &pods[i], idx)
	}

	events, err := m.client.watchPods(ctx, t.namespace, selector)
	if err != nil {
		return err
	}

	knownRunning := map[string]bool{}
	for _, p := range pods {
		knownRunning[p.Name] = true
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			switch ev.kind {
			case podAdded:
				if !knownRunning[ev.pod.Name] {
					knownRunning[ev.pod.Name] = true
					m.logger.Info("new pod detected", "pod", ev.pod.Name, "svc", t.resourceName)
					m.addPod(ctx, ev.pod, idx)
				}
			case podDeleted:
				if knownRunning[ev.pod.Name] {
					delete(knownRunning, ev.pod.Name)
					m.removePod(ev.pod.Name)
				}
			}
		}
	}
}

