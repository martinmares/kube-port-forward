package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
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
	owners     map[string]struct{}
	resources  map[string]struct{}
	cancel     context.CancelFunc
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
	mu            sync.RWMutex
	entries       []*podEntry // append-only, terminated entries kept for UI
	reservedPorts map[int]struct{}
	cfg           *config
	client        *k8sClient
	logger        *slog.Logger
	notify        chan struct{} // closed+recreated on state change to notify SSE clients
	notifyMu      sync.Mutex
}

func newForwardManager(cfg *config, client *k8sClient, logger *slog.Logger) *forwardManager {
	return &forwardManager{
		cfg:           cfg,
		client:        client,
		logger:        logger,
		reservedPorts: make(map[int]struct{}),
		notify:        make(chan struct{}),
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

func targetOwner(t target) string {
	return fmt.Sprintf("%s/%s:%d", t.namespace, t.resourceStr(), t.remotePort)
}

// registerPod creates an entry unless the pod/remote-port pair is already active.
// Duplicate resources become owners of the existing forward.
func (m *forwardManager) registerPod(pod *corev1.Pod, t target) (*podEntry, bool, error) {
	owner := targetOwner(t)
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, e := range m.entries {
		e.mu.Lock()
		if e.namespace == pod.Namespace && e.podName == pod.Name &&
			e.remotePort == t.remotePort && e.status != statusTerminated {
			e.owners[owner] = struct{}{}
			e.resources[t.resourceStr()] = struct{}{}
			e.mu.Unlock()
			return e, false, nil
		}
		e.mu.Unlock()
	}

	localPort, err := m.allocateLocalPortLocked(t.localPort)
	if err != nil {
		return nil, false, err
	}
	entry := &podEntry{
		podName:    pod.Name,
		namespace:  pod.Namespace,
		localPort:  localPort,
		remotePort: t.remotePort,
		status:     statusConnecting,
		meta:       extractMeta(pod),
		owners:     map[string]struct{}{owner: {}},
		resources:  map[string]struct{}{t.resourceStr(): {}},
	}
	m.entries = append(m.entries, entry)
	return entry, true, nil
}

func (m *forwardManager) allocateLocalPortLocked(start int) (int, error) {
	for port := start; port <= 65535; port++ {
		if _, reserved := m.reservedPorts[port]; reserved {
			continue
		}
		m.reservedPorts[port] = struct{}{}
		return port, nil
	}
	return 0, fmt.Errorf("no free local port available at or above %d", start)
}

// addPod starts a port-forward goroutine for a pod if not already tracked.
func (m *forwardManager) addPod(ctx context.Context, pod *corev1.Pod, t target) error {
	entry, added, err := m.registerPod(pod, t)
	if err != nil {
		return err
	}
	if !added {
		m.logger.Debug("reusing existing forward",
			"pod", pod.Name,
			"remote_port", t.remotePort,
			"resource", t.resourceStr(),
		)
		return nil
	}

	m.logger.Info("adding pod",
		"pod", pod.Name,
		"local_port", entry.localPort,
		"remote_port", entry.remotePort,
		"resource", t.resourceStr(),
	)
	m.notifyClients()

	forwardCtx, cancel := context.WithCancel(ctx)
	entry.mu.Lock()
	entry.cancel = cancel
	entry.mu.Unlock()
	go m.runForward(forwardCtx, entry)
	return nil
}

// removePodOwner drops a resource's ownership and terminates forwards with no owners left.
func (m *forwardManager) removePodOwner(namespace, podName string, t target) {
	owner := targetOwner(t)
	changed := false
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, e := range m.entries {
		e.mu.Lock()
		if e.namespace == namespace && e.podName == podName && e.remotePort == t.remotePort {
			if _, owned := e.owners[owner]; owned {
				delete(e.owners, owner)
			}
			if len(e.owners) == 0 && e.status != statusTerminated {
				e.status = statusTerminated
				if e.cancel != nil {
					e.cancel()
				}
				changed = true
				m.logger.Info("pod terminated",
					"pod", podName,
					"local_port", e.localPort,
					"remote_port", e.remotePort,
				)
			}
		}
		e.mu.Unlock()
	}
	if changed {
		m.notifyClients()
	}
}

func (m *forwardManager) snapshot() []podSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]podSnapshot, len(m.entries))
	for i, e := range m.entries {
		e.mu.RLock()
		resources := make([]string, 0, len(e.resources))
		for resource := range e.resources {
			resources = append(resources, resource)
		}
		sort.Strings(resources)
		out[i] = podSnapshot{
			podName:    e.podName,
			namespace:  e.namespace,
			localPort:  e.localPort,
			remotePort: e.remotePort,
			status:     e.status,
			attempt:    e.attempt,
			meta:       e.meta,
			resource:   strings.Join(resources, ", "),
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
	for _, t := range m.cfg.targets {
		go func(t target) {
			errCh <- m.watchTarget(ctx, t)
		}(t)
	}
	// wait for first error or context cancel
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return nil
	}
}

func (m *forwardManager) watchTarget(ctx context.Context, t target) error {
	switch t.resourceKind {
	case resourcePod:
		pod, err := m.client.getPod(ctx, t.namespace, t.resourceName, m.logger)
		if err != nil {
			return err
		}
		if err := m.addPod(ctx, pod, t); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	case resourceService:
		if t.resourceName == "*" {
			return m.watchAllServices(ctx, t)
		}
		selector, err := m.client.resolveService(ctx, t.namespace, t.resourceName)
		if err != nil {
			return err
		}
		return m.watchSelector(ctx, t, selector)
	case resourceDeployment:
		selector, err := m.client.resolveDeployment(ctx, t.namespace, t.resourceName)
		if err != nil {
			return err
		}
		return m.watchSelector(ctx, t, selector)
	default:
		return fmt.Errorf("unsupported resource kind %d", t.resourceKind)
	}
}

func (m *forwardManager) watchAllServices(ctx context.Context, parent target) error {
	services, err := m.client.listServices(ctx, parent.namespace)
	if err != nil {
		return err
	}
	eligible := 0
	for i := range services {
		svc := &services[i]
		if len(svc.Spec.Selector) == 0 {
			m.logger.Info("skipping service without pod selector", "svc", svc.Name)
			continue
		}
		eligible++
		t := parent
		t.resourceName = svc.Name
		selector := labels.SelectorFromSet(svc.Spec.Selector)
		go func() {
			if err := m.watchSelector(ctx, t, selector); err != nil && ctx.Err() == nil {
				m.logger.Warn("skipping service",
					"svc", t.resourceName,
					"err", err,
				)
			}
		}()
	}
	m.logger.Info("watching all services",
		"namespace", parent.namespace,
		"services", eligible,
	)
	<-ctx.Done()
	return nil
}

func (m *forwardManager) watchSelector(ctx context.Context, t target, selector labels.Selector) error {
	pods, err := m.client.listPods(ctx, t.namespace, selector)
	if err != nil {
		return err
	}

	targets := []target{t}
	if t.remotePort == 0 {
		if len(pods) == 0 {
			return fmt.Errorf("%s has no running pods", t.resourceStr())
		}
		ports := containerPorts(&pods[0])
		if len(ports) == 0 {
			return fmt.Errorf("%s has no containerPorts in matching pods", t.resourceStr())
		}
		targets = make([]target, len(ports))
		for i, remotePort := range ports {
			targets[i] = t
			targets[i].localPort = t.localPort + i
			targets[i].remotePort = remotePort
		}
		m.logger.Info("expanding wildcard ports",
			"resource", t.resourceStr(),
			"ports", ports,
		)
	}

	m.logger.Info("resolved resource",
		"resource", t.resourceStr(),
		"selector", selector.String(),
		"pods", len(pods),
	)
	for i := range pods {
		for _, concreteTarget := range targets {
			if err := m.addPod(ctx, &pods[i], concreteTarget); err != nil {
				return err
			}
		}
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
					m.logger.Info("new pod detected",
						"pod", ev.pod.Name,
						"resource", t.resourceStr(),
					)
					for _, concreteTarget := range targets {
						if err := m.addPod(ctx, ev.pod, concreteTarget); err != nil {
							return err
						}
					}
				}
			case podDeleted:
				if knownRunning[ev.pod.Name] {
					delete(knownRunning, ev.pod.Name)
					for _, concreteTarget := range targets {
						m.removePodOwner(ev.pod.Namespace, ev.pod.Name, concreteTarget)
					}
				}
			}
		}
	}
}

func containerPorts(pod *corev1.Pod) []int {
	seen := map[int]struct{}{}
	var ports []int
	for _, container := range pod.Spec.Containers {
		for _, p := range container.Ports {
			port := int(p.ContainerPort)
			if _, exists := seen[port]; exists {
				continue
			}
			seen[port] = struct{}{}
			ports = append(ports, port)
		}
	}
	sort.Ints(ports)
	return ports
}
