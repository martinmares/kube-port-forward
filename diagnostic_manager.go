package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mares/kube-port-forward/internal/diagnostic"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

const (
	diagnosticConfigKey = "config.yaml"
	diagnosticTokenKey  = "token"
)

type diagnosticManager struct {
	mu         sync.RWMutex
	opMu       sync.Mutex
	rootCtx    context.Context
	client     *k8sClient
	namespace  string
	logger     *slog.Logger
	notify     func()
	profile    *diagnostic.Profile
	configPath string

	status        string
	statusReason  string
	statusMessage string
	errorMessage  string
	podName       string
	configMap     string
	secretName    string
	token         string
	proxyAddress  string
	startedAt     time.Time
	expiresAt     time.Time
	finishedAt    time.Time
	sessionCancel context.CancelFunc
	tunnels       map[string]*diagnosticTunnel
}

type diagnosticTunnel struct {
	id            string
	name          string
	host          string
	port          int
	mode          string
	tlsSkipVerify bool
	localPort     int
	listener      net.Listener
	close         func()
}

type diagnosticSnapshot struct {
	Configured       bool                       `json:"configured"`
	ConfigPath       string                     `json:"configPath,omitempty"`
	Status           string                     `json:"status"`
	StatusReason     string                     `json:"statusReason,omitempty"`
	StatusMessage    string                     `json:"statusMessage,omitempty"`
	Error            string                     `json:"error,omitempty"`
	Namespace        string                     `json:"namespace"`
	PodName          string                     `json:"podName,omitempty"`
	ConfigMap        string                     `json:"configMap,omitempty"`
	Image            string                     `json:"image,omitempty"`
	PullPolicy       string                     `json:"pullPolicy,omitempty"`
	ImagePullSecrets []string                   `json:"imagePullSecrets,omitempty"`
	TTL              string                     `json:"ttl,omitempty"`
	MaxConnections   int                        `json:"maxConnections,omitempty"`
	ConnectTimeout   string                     `json:"connectTimeout,omitempty"`
	IdleTimeout      string                     `json:"idleTimeout,omitempty"`
	PolicyYAML       string                     `json:"policyYAML,omitempty"`
	StartedAt        string                     `json:"startedAt,omitempty"`
	ExpiresAt        string                     `json:"expiresAt,omitempty"`
	FinishedAt       string                     `json:"finishedAt,omitempty"`
	Tunnels          []diagnosticTunnelSnapshot `json:"tunnels"`
}

type diagnosticTunnelSnapshot struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Mode          string `json:"mode"`
	TLSSkipVerify bool   `json:"tlsSkipVerify"`
	LocalPort     int    `json:"localPort"`
	Address       string `json:"address"`
	URL           string `json:"url,omitempty"`
}

func newDiagnosticManager(ctx context.Context, cfg *config, client *k8sClient, logger *slog.Logger, notify func()) *diagnosticManager {
	manager := &diagnosticManager{
		rootCtx:    ctx,
		client:     client,
		namespace:  cfg.namespace,
		logger:     logger,
		notify:     notify,
		profile:    cfg.diagnosticProfile,
		configPath: cfg.diagnosticConfigPath,
		status:     "stopped",
		tunnels:    make(map[string]*diagnosticTunnel),
	}
	return manager
}

func (m *diagnosticManager) snapshot() diagnosticSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshot := diagnosticSnapshot{
		Configured:    m.profile != nil,
		ConfigPath:    m.configPath,
		Status:        m.status,
		StatusReason:  m.statusReason,
		StatusMessage: m.statusMessage,
		Error:         m.errorMessage,
		Namespace:     m.namespace,
		PodName:       m.podName,
		ConfigMap:     m.configMap,
		Tunnels:       make([]diagnosticTunnelSnapshot, 0, len(m.tunnels)),
	}
	if !m.startedAt.IsZero() {
		snapshot.StartedAt = m.startedAt.UTC().Format(time.RFC3339)
	}
	if !m.expiresAt.IsZero() {
		snapshot.ExpiresAt = m.expiresAt.UTC().Format(time.RFC3339)
	}
	if !m.finishedAt.IsZero() {
		snapshot.FinishedAt = m.finishedAt.UTC().Format(time.RFC3339)
	}
	if m.profile != nil {
		snapshot.Image = m.profile.Image
		snapshot.PullPolicy = m.profile.PullPolicy
		snapshot.ImagePullSecrets = append([]string(nil), m.profile.ImagePullSecrets...)
		snapshot.TTL = m.profile.TTL
		snapshot.MaxConnections = m.profile.MaxConnections
		snapshot.ConnectTimeout = m.profile.ConnectTimeout
		snapshot.IdleTimeout = m.profile.IdleTimeout
		if policyYAML, err := diagnostic.MarshalPolicyYAML(m.profile.Policy); err == nil {
			snapshot.PolicyYAML = policyYAML
		}
	}
	for _, tunnel := range m.tunnels {
		item := diagnosticTunnelSnapshot{
			ID:            tunnel.id,
			Name:          tunnel.name,
			Host:          tunnel.host,
			Port:          tunnel.port,
			Mode:          tunnel.mode,
			TLSSkipVerify: tunnel.tlsSkipVerify,
			LocalPort:     tunnel.localPort,
			Address:       fmt.Sprintf("127.0.0.1:%d", tunnel.localPort),
		}
		if tunnel.mode == "http" || tunnel.mode == "https" {
			item.URL = "http://" + item.Address
		}
		snapshot.Tunnels = append(snapshot.Tunnels, item)
	}
	sort.Slice(snapshot.Tunnels, func(i, j int) bool {
		return snapshot.Tunnels[i].LocalPort < snapshot.Tunnels[j].LocalPort
	})
	return snapshot
}

func (m *diagnosticManager) start(profile diagnostic.Profile) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	profile.SetDefaults()
	if err := profile.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	if m.profile == nil {
		m.mu.Unlock()
		return fmt.Errorf("diagnostic gateway is not configured; use --diagnostic-config")
	}
	if m.status == "starting" || m.status == "ready" || m.status == "stopping" {
		m.mu.Unlock()
		return fmt.Errorf("diagnostic gateway is already %s", m.status)
	}
	m.profile = &profile
	m.status = "starting"
	m.statusReason = ""
	m.statusMessage = ""
	m.errorMessage = ""
	m.startedAt = time.Time{}
	m.expiresAt = time.Time{}
	m.finishedAt = time.Time{}
	m.mu.Unlock()
	m.changed()

	sessionID, err := randomID(6)
	if err != nil {
		return m.failStart(err)
	}
	token, err := randomToken()
	if err != nil {
		return m.failStart(err)
	}
	name := "kpf-diagnostic-" + sessionID
	runtimeYAML, err := profile.RuntimeConfig().YAML()
	if err != nil {
		return m.failStart(err)
	}
	policyHash := sha256.Sum256(runtimeYAML)
	configMap := buildDiagnosticConfigMap(m.namespace, name, string(runtimeYAML), hex.EncodeToString(policyHash[:8]))

	createdConfigMap, err := m.client.clientset.CoreV1().ConfigMaps(m.namespace).Create(m.rootCtx, configMap, metav1.CreateOptions{})
	if err != nil {
		return m.failStart(fmt.Errorf("create diagnostic ConfigMap: %w", err))
	}
	secret := buildDiagnosticSecret(m.namespace, name, token)
	createdSecret, err := m.client.clientset.CoreV1().Secrets(m.namespace).Create(m.rootCtx, secret, metav1.CreateOptions{})
	if err != nil {
		_ = m.client.clientset.CoreV1().ConfigMaps(m.namespace).Delete(context.Background(), createdConfigMap.Name, metav1.DeleteOptions{})
		return m.failStart(fmt.Errorf("create diagnostic Secret: %w", err))
	}
	pod := buildDiagnosticPod(m.namespace, name, createdConfigMap.Name, createdSecret.Name, profile)
	createdPod, err := m.client.clientset.CoreV1().Pods(m.namespace).Create(m.rootCtx, pod, metav1.CreateOptions{})
	if err != nil {
		_ = m.client.clientset.CoreV1().Secrets(m.namespace).Delete(context.Background(), createdSecret.Name, metav1.DeleteOptions{})
		_ = m.client.clientset.CoreV1().ConfigMaps(m.namespace).Delete(context.Background(), createdConfigMap.Name, metav1.DeleteOptions{})
		return m.failStart(fmt.Errorf("create diagnostic pod: %w", err))
	}

	createdConfigMap.OwnerReferences = []metav1.OwnerReference{diagnosticOwnerReference(createdPod)}
	if _, err := m.client.clientset.CoreV1().ConfigMaps(m.namespace).Update(m.rootCtx, createdConfigMap, metav1.UpdateOptions{}); err != nil {
		m.logger.Warn("could not attach ConfigMap owner reference", "configmap", createdConfigMap.Name, "err", err)
	}
	createdSecret.OwnerReferences = append([]metav1.OwnerReference(nil), createdConfigMap.OwnerReferences...)
	if _, err := m.client.clientset.CoreV1().Secrets(m.namespace).Update(m.rootCtx, createdSecret, metav1.UpdateOptions{}); err != nil {
		m.logger.Warn("could not attach Secret owner reference", "secret", createdSecret.Name, "err", err)
	}

	sessionCtx, cancel := context.WithCancel(m.rootCtx)
	m.mu.Lock()
	m.podName = createdPod.Name
	m.configMap = createdConfigMap.Name
	m.secretName = createdSecret.Name
	m.token = token
	m.sessionCancel = cancel
	m.startedAt = createdPod.CreationTimestamp.Time
	if m.startedAt.IsZero() {
		m.startedAt = time.Now()
	}
	m.expiresAt = m.startedAt.Add(profile.TTLDuration())
	m.mu.Unlock()
	m.changed()
	m.logger.Info("diagnostic pod created", "pod", createdPod.Name, "configmap", createdConfigMap.Name, "secret", createdSecret.Name)
	go m.runSession(sessionCtx, createdPod.Name)
	return nil
}

func (m *diagnosticManager) failStart(err error) error {
	m.mu.Lock()
	m.status = "error"
	m.statusReason = "Start failed"
	m.statusMessage = "The diagnostic gateway could not be started."
	m.errorMessage = err.Error()
	m.finishedAt = time.Now()
	m.mu.Unlock()
	m.changed()
	return err
}

func (m *diagnosticManager) runSession(ctx context.Context, podName string) {
	if err := m.waitForReady(ctx, podName); err != nil {
		if ctx.Err() == nil {
			if termination, ok := m.detectDiagnosticTermination(ctx, podName, 0); ok {
				m.setTerminal(podName, termination)
				m.cleanupTerminalResources(podName)
			} else {
				m.setError(err)
			}
		}
		return
	}
	localPort, done, stop, err := m.startProxyForward(ctx, podName)
	if err != nil {
		if ctx.Err() == nil {
			m.setError(err)
		}
		return
	}
	defer stop()

	m.mu.Lock()
	m.proxyAddress = fmt.Sprintf("127.0.0.1:%d", localPort)
	m.status = "ready"
	m.statusReason = ""
	m.statusMessage = ""
	m.errorMessage = ""
	m.mu.Unlock()
	m.changed()
	m.logger.Info("diagnostic gateway ready", "pod", podName)

	monitor := time.NewTicker(5 * time.Second)
	defer monitor.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-done:
			if ctx.Err() != nil {
				return
			}
			m.closeTunnels()
			if termination, ok := m.detectDiagnosticTermination(ctx, podName, 5*time.Second); ok {
				m.setTerminal(podName, termination)
				m.cleanupTerminalResources(podName)
			} else if err == nil {
				m.setError(fmt.Errorf("diagnostic pod port-forward stopped unexpectedly"))
			} else {
				m.setError(fmt.Errorf("diagnostic pod port-forward stopped: %w", err))
			}
			return
		case <-monitor.C:
			termination, terminal, err := m.inspectDiagnosticPod(ctx, podName)
			if err != nil {
				m.logger.Warn("could not inspect diagnostic pod", "pod", podName, "err", err)
				continue
			}
			if terminal {
				m.closeTunnels()
				m.setTerminal(podName, termination)
				m.cleanupTerminalResources(podName)
				return
			}
		}
	}
}

type diagnosticTermination struct {
	status     string
	reason     string
	message    string
	finishedAt time.Time
}

func (m *diagnosticManager) inspectDiagnosticPod(ctx context.Context, podName string) (diagnosticTermination, bool, error) {
	pod, err := m.client.clientset.CoreV1().Pods(m.namespace).Get(ctx, podName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return diagnosticTermination{
			status: "terminated", reason: "Pod removed",
			message:    fmt.Sprintf("Diagnostic Pod %q no longer exists in namespace %q. Start a new gateway to continue.", podName, m.namespace),
			finishedAt: time.Now(),
		}, true, nil
	}
	if err != nil {
		return diagnosticTermination{}, false, err
	}
	termination, terminal := diagnosticPodTermination(pod, m.profileTTL())
	return termination, terminal, nil
}

func (m *diagnosticManager) detectDiagnosticTermination(ctx context.Context, podName string, wait time.Duration) (diagnosticTermination, bool) {
	deadline := time.Now().Add(wait)
	for {
		termination, terminal, err := m.inspectDiagnosticPod(ctx, podName)
		if err == nil && terminal {
			return termination, true
		}
		if wait <= 0 || time.Now().After(deadline) || ctx.Err() != nil {
			return diagnosticTermination{}, false
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return diagnosticTermination{}, false
		case <-timer.C:
		}
	}
}

func (m *diagnosticManager) profileTTL() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.profile == nil {
		return ""
	}
	return m.profile.TTL
}

func diagnosticPodTermination(pod *corev1.Pod, ttl string) (diagnosticTermination, bool) {
	if pod.DeletionTimestamp != nil {
		return diagnosticTermination{
			status: "terminated", reason: "Pod terminating",
			message:    fmt.Sprintf("Diagnostic Pod %q is being terminated by Kubernetes. Start a new gateway to continue.", pod.Name),
			finishedAt: time.Now(),
		}, true
	}
	if pod.Status.Phase != corev1.PodFailed && pod.Status.Phase != corev1.PodSucceeded {
		return diagnosticTermination{}, false
	}

	finishedAt := diagnosticPodFinishedAt(pod)
	if finishedAt.IsZero() {
		finishedAt = time.Now()
	}
	if pod.Status.Reason == "DeadlineExceeded" {
		lifetime := ttl
		if lifetime == "" {
			lifetime = "the configured limit"
		}
		return diagnosticTermination{
			status: "expired", reason: "Maximum lifetime reached",
			message:    fmt.Sprintf("Diagnostic Pod %q reached its maximum lifetime (%s) and was terminated by Kubernetes. Start a new gateway to continue.", pod.Name, lifetime),
			finishedAt: finishedAt,
		}, true
	}

	reason := strings.TrimSpace(pod.Status.Reason)
	if reason == "" {
		reason = string(pod.Status.Phase)
	}
	message := fmt.Sprintf("Diagnostic Pod %q finished with phase %s", pod.Name, pod.Status.Phase)
	if pod.Status.Reason != "" {
		message += " (" + pod.Status.Reason + ")"
	}
	if detail := strings.TrimSpace(pod.Status.Message); detail != "" {
		message += ": " + detail
	} else {
		message += "."
	}
	message += " Start a new gateway to continue."
	return diagnosticTermination{status: "terminated", reason: reason, message: message, finishedAt: finishedAt}, true
}

func diagnosticPodFinishedAt(pod *corev1.Pod) time.Time {
	var finishedAt time.Time
	for _, container := range pod.Status.ContainerStatuses {
		if terminated := container.State.Terminated; terminated != nil && terminated.FinishedAt.Time.After(finishedAt) {
			finishedAt = terminated.FinishedAt.Time
		}
	}
	return finishedAt
}

func (m *diagnosticManager) waitForReady(ctx context.Context, podName string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		pod, err := m.client.clientset.CoreV1().Pods(m.namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get diagnostic pod: %w", err)
		}
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			return fmt.Errorf("diagnostic pod finished with phase %s", pod.Status.Phase)
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == "proxy" && status.Ready {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *diagnosticManager) startProxyForward(ctx context.Context, podName string) (int, <-chan error, func(), error) {
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", m.namespace, podName)
	hostURL, err := url.Parse(m.client.config.Host)
	if err != nil {
		return 0, nil, nil, err
	}
	hostURL.Path = path
	transport, upgrader, err := spdy.RoundTripperFor(m.client.config)
	if err != nil {
		return 0, nil, nil, err
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, hostURL)
	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	done := make(chan error, 1)
	forwarder, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{"0:" + strconv.Itoa(diagnostic.DefaultProxyPort)}, stopCh, readyCh, nil, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	go func() { done <- forwarder.ForwardPorts() }()
	select {
	case <-ctx.Done():
		close(stopCh)
		return 0, nil, nil, ctx.Err()
	case err := <-done:
		if err == nil {
			err = fmt.Errorf("diagnostic pod port-forward stopped before becoming ready")
		}
		return 0, nil, nil, err
	case <-readyCh:
	}
	ports, err := forwarder.GetPorts()
	if err != nil {
		close(stopCh)
		return 0, nil, nil, fmt.Errorf("determine diagnostic proxy port: %w", err)
	}
	if len(ports) != 1 {
		close(stopCh)
		return 0, nil, nil, fmt.Errorf("determine diagnostic proxy port: expected one forwarded port, got %d", len(ports))
	}
	var once sync.Once
	stop := func() { once.Do(func() { close(stopCh) }) }
	return int(ports[0].Local), done, stop, nil
}

func (m *diagnosticManager) stop() error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if m.status == "stopped" {
		m.mu.Unlock()
		return nil
	}
	m.status = "stopping"
	cancel := m.sessionCancel
	podName := m.podName
	configMap := m.configMap
	secretName := m.secretName
	m.mu.Unlock()
	m.changed()
	if cancel != nil {
		cancel()
	}
	m.closeTunnels()

	ctx, cancelDelete := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelDelete()
	var firstErr error
	if podName != "" {
		grace := int64(0)
		if err := m.client.clientset.CoreV1().Pods(m.namespace).Delete(ctx, podName, metav1.DeleteOptions{GracePeriodSeconds: &grace}); err != nil && !apierrors.IsNotFound(err) {
			firstErr = fmt.Errorf("delete diagnostic pod: %w", err)
		}
	}
	if configMap != "" {
		if err := m.client.clientset.CoreV1().ConfigMaps(m.namespace).Delete(ctx, configMap, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) && firstErr == nil {
			firstErr = fmt.Errorf("delete diagnostic ConfigMap: %w", err)
		}
	}
	if secretName != "" {
		if err := m.client.clientset.CoreV1().Secrets(m.namespace).Delete(ctx, secretName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) && firstErr == nil {
			firstErr = fmt.Errorf("delete diagnostic Secret: %w", err)
		}
	}
	if firstErr == nil {
		firstErr = m.waitForResourcesDeleted(ctx, podName, configMap, secretName)
	}
	if firstErr != nil {
		m.mu.Lock()
		m.status = "error"
		m.statusReason = "Cleanup failed"
		m.statusMessage = "The diagnostic resources could not be removed completely."
		m.errorMessage = firstErr.Error()
		m.proxyAddress = ""
		m.sessionCancel = nil
		m.mu.Unlock()
		m.changed()
		return firstErr
	}

	m.mu.Lock()
	m.status = "stopped"
	m.statusReason = ""
	m.statusMessage = ""
	m.errorMessage = ""
	m.podName = ""
	m.configMap = ""
	m.secretName = ""
	m.token = ""
	m.proxyAddress = ""
	m.startedAt = time.Time{}
	m.expiresAt = time.Time{}
	m.finishedAt = time.Time{}
	m.sessionCancel = nil
	m.mu.Unlock()
	m.changed()
	return firstErr
}

func (m *diagnosticManager) waitForResourcesDeleted(ctx context.Context, podName, configMap, secretName string) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		remaining := make([]string, 0, 3)
		if podName != "" {
			if _, err := m.client.clientset.CoreV1().Pods(m.namespace).Get(ctx, podName, metav1.GetOptions{}); err == nil {
				remaining = append(remaining, "pod/"+podName)
			} else if !apierrors.IsNotFound(err) {
				return fmt.Errorf("verify diagnostic pod deletion: %w", err)
			}
		}
		if configMap != "" {
			if _, err := m.client.clientset.CoreV1().ConfigMaps(m.namespace).Get(ctx, configMap, metav1.GetOptions{}); err == nil {
				remaining = append(remaining, "configmap/"+configMap)
			} else if !apierrors.IsNotFound(err) {
				return fmt.Errorf("verify diagnostic ConfigMap deletion: %w", err)
			}
		}
		if secretName != "" {
			if _, err := m.client.clientset.CoreV1().Secrets(m.namespace).Get(ctx, secretName, metav1.GetOptions{}); err == nil {
				remaining = append(remaining, "secret/"+secretName)
			} else if !apierrors.IsNotFound(err) {
				return fmt.Errorf("verify diagnostic Secret deletion: %w", err)
			}
		}
		if len(remaining) == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for diagnostic resource deletion (%s): %w", strings.Join(remaining, ", "), ctx.Err())
		case <-ticker.C:
		}
	}
}

func (m *diagnosticManager) addTunnel(name, host string, port, requestedLocalPort int, mode string, tlsSkipVerify bool) (diagnosticTunnelSnapshot, error) {
	host = strings.TrimSpace(host)
	name = strings.TrimSpace(name)
	if host == "" || strings.ContainsAny(host, " \t\r\n/\\") || (strings.Contains(host, ":") && net.ParseIP(host) == nil) {
		return diagnosticTunnelSnapshot{}, fmt.Errorf("invalid target hostname")
	}
	if port < 1 || port > 65535 {
		return diagnosticTunnelSnapshot{}, fmt.Errorf("target port must be between 1 and 65535")
	}
	if requestedLocalPort < 0 || requestedLocalPort > 65535 {
		return diagnosticTunnelSnapshot{}, fmt.Errorf("local port must be between 0 and 65535")
	}
	if mode != "tcp" && mode != "http" && mode != "https" {
		return diagnosticTunnelSnapshot{}, fmt.Errorf("mode must be tcp, http, or https")
	}
	if tlsSkipVerify && mode != "https" {
		return diagnosticTunnelSnapshot{}, fmt.Errorf("TLS verification can be skipped only in HTTPS mode")
	}
	if name == "" {
		name = net.JoinHostPort(host, strconv.Itoa(port))
	}

	m.mu.RLock()
	if m.status != "ready" {
		m.mu.RUnlock()
		return diagnosticTunnelSnapshot{}, fmt.Errorf("diagnostic gateway is not ready")
	}
	proxyAddress := m.proxyAddress
	token := m.token
	profile := *m.profile
	m.mu.RUnlock()

	connectTimeout, _ := time.ParseDuration(profile.ConnectTimeout)
	testCtx, cancelTest := context.WithTimeout(m.rootCtx, connectTimeout)
	testConnection, err := diagnostic.ConnectThroughProxy(testCtx, proxyAddress, token, net.JoinHostPort(host, strconv.Itoa(port)))
	cancelTest()
	if err != nil {
		return diagnosticTunnelSnapshot{}, fmt.Errorf("target connectivity test failed: %w", err)
	}
	_ = testConnection.Close()

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", requestedLocalPort))
	if err != nil {
		return diagnosticTunnelSnapshot{}, fmt.Errorf("listen on local port: %w", err)
	}
	localPort := listener.Addr().(*net.TCPAddr).Port
	id, err := randomID(8)
	if err != nil {
		listener.Close()
		return diagnosticTunnelSnapshot{}, err
	}
	tunnel := &diagnosticTunnel{
		id: id, name: name, host: host, port: port, mode: mode,
		tlsSkipVerify: tlsSkipVerify, localPort: localPort, listener: listener,
	}
	if mode == "http" || mode == "https" {
		idleTimeout, _ := time.ParseDuration(profile.IdleTimeout)
		var tlsConfig *tls.Config
		if mode == "https" {
			tlsConfig = &tls.Config{
				MinVersion: tls.VersionTLS12,
				ServerName: tlsServerName(host),
				// The user must explicitly opt in for a tunnel whose upstream uses an untrusted certificate.
				InsecureSkipVerify: tlsSkipVerify, //nolint:gosec
			}
		}
		transport := &http.Transport{
			Proxy:           nil,
			TLSClientConfig: tlsConfig,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return diagnostic.ConnectThroughProxy(ctx, proxyAddress, token, net.JoinHostPort(host, strconv.Itoa(port)))
			},
			TLSHandshakeTimeout: connectTimeout,
			ForceAttemptHTTP2:   true,
		}
		proxy := &httputil.ReverseProxy{
			Director: func(request *http.Request) {
				request.URL.Scheme = mode
				request.URL.Host = net.JoinHostPort(host, strconv.Itoa(port))
				request.Host = upstreamHostHeader(host, port, mode)
			},
			Transport: transport,
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				m.logger.Warn("diagnostic HTTP tunnel failed", "target", net.JoinHostPort(host, strconv.Itoa(port)), "err", err)
				http.Error(w, "diagnostic target unavailable", http.StatusBadGateway)
			},
		}
		server := &http.Server{
			Handler:           proxy,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       idleTimeout,
			MaxHeaderBytes:    1 << 20,
		}
		tunnel.close = func() {
			_ = listener.Close()
			transport.CloseIdleConnections()
		}
		go func() {
			if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
				m.logger.Warn("diagnostic HTTP listener stopped", "local_port", localPort, "err", err)
			}
		}()
	} else {
		tunnel.close = func() { _ = listener.Close() }
	}
	m.mu.Lock()
	if m.status != "ready" {
		m.mu.Unlock()
		tunnel.close()
		return diagnosticTunnelSnapshot{}, fmt.Errorf("diagnostic gateway is not ready")
	}
	m.tunnels[id] = tunnel
	m.mu.Unlock()
	m.changed()
	if mode == "tcp" {
		go m.serveTunnel(tunnel, proxyAddress, token, profile.ConnectTimeout)
	}
	m.logger.Info("diagnostic tunnel created", "local_port", localPort, "target", net.JoinHostPort(host, strconv.Itoa(port)), "mode", mode)
	return tunnelSnapshot(tunnel), nil
}

func (m *diagnosticManager) serveTunnel(tunnel *diagnosticTunnel, proxyAddress, token, timeoutValue string) {
	timeout, _ := time.ParseDuration(timeoutValue)
	target := net.JoinHostPort(tunnel.host, strconv.Itoa(tunnel.port))
	for {
		client, err := tunnel.listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer client.Close()
			ctx, cancel := context.WithTimeout(m.rootCtx, timeout)
			upstream, err := diagnostic.ConnectThroughProxy(ctx, proxyAddress, token, target)
			cancel()
			if err != nil {
				m.logger.Warn("diagnostic tunnel connection failed", "target", target, "err", err)
				return
			}
			defer upstream.Close()
			copyBoth(client, upstream)
		}()
	}
}

func copyBoth(left, right net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(left, right); done <- struct{}{} }()
	go func() { _, _ = io.Copy(right, left); done <- struct{}{} }()
	<-done
	_ = left.Close()
	_ = right.Close()
	<-done
}

func (m *diagnosticManager) removeTunnel(id string) error {
	m.mu.Lock()
	tunnel, ok := m.tunnels[id]
	if ok {
		delete(m.tunnels, id)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("diagnostic tunnel not found")
	}
	tunnel.close()
	m.changed()
	return nil
}

func (m *diagnosticManager) closeTunnels() {
	m.mu.Lock()
	tunnels := make([]*diagnosticTunnel, 0, len(m.tunnels))
	for _, tunnel := range m.tunnels {
		tunnels = append(tunnels, tunnel)
	}
	m.tunnels = make(map[string]*diagnosticTunnel)
	m.mu.Unlock()
	for _, tunnel := range tunnels {
		tunnel.close()
	}
	m.changed()
}

func (m *diagnosticManager) setError(err error) {
	m.mu.Lock()
	m.status = "error"
	m.statusReason = "Gateway connection lost"
	m.statusMessage = "The connection to the diagnostic Pod ended unexpectedly. The Pod may still require cleanup."
	m.errorMessage = err.Error()
	m.finishedAt = time.Now()
	m.proxyAddress = ""
	m.mu.Unlock()
	m.changed()
	m.logger.Error("diagnostic gateway failed", "err", err)
}

func (m *diagnosticManager) setTerminal(podName string, termination diagnosticTermination) {
	m.mu.Lock()
	if m.podName != podName {
		m.mu.Unlock()
		return
	}
	m.status = termination.status
	m.statusReason = termination.reason
	m.statusMessage = termination.message
	m.errorMessage = ""
	m.finishedAt = termination.finishedAt
	m.proxyAddress = ""
	m.token = ""
	m.sessionCancel = nil
	m.mu.Unlock()
	m.changed()
	m.logger.Info("diagnostic pod terminated", "pod", podName, "status", termination.status, "reason", termination.reason)
}

func (m *diagnosticManager) cleanupTerminalResources(podName string) {
	m.mu.RLock()
	if m.podName != podName {
		m.mu.RUnlock()
		return
	}
	configMap := m.configMap
	secretName := m.secretName
	m.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	grace := int64(0)
	resources := []struct {
		kind   string
		name   string
		remove func() error
	}{
		{kind: "Pod", name: podName, remove: func() error {
			return m.client.clientset.CoreV1().Pods(m.namespace).Delete(ctx, podName, metav1.DeleteOptions{GracePeriodSeconds: &grace})
		}},
		{kind: "ConfigMap", name: configMap, remove: func() error {
			return m.client.clientset.CoreV1().ConfigMaps(m.namespace).Delete(ctx, configMap, metav1.DeleteOptions{})
		}},
		{kind: "Secret", name: secretName, remove: func() error {
			return m.client.clientset.CoreV1().Secrets(m.namespace).Delete(ctx, secretName, metav1.DeleteOptions{})
		}},
	}
	for _, resource := range resources {
		if resource.name == "" {
			continue
		}
		if err := resource.remove(); err != nil && !apierrors.IsNotFound(err) {
			m.logger.Warn("could not clean up terminated diagnostic resource", "kind", resource.kind, "name", resource.name, "err", err)
		}
	}
}

func (m *diagnosticManager) changed() {
	if m.notify != nil {
		m.notify()
	}
}

func buildDiagnosticConfigMap(namespace, name, runtimeYAML, policyHash string) *corev1.ConfigMap {
	immutable := true
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "kube-port-forward-proxy",
				"app.kubernetes.io/managed-by": "kube-port-forward",
			},
			Annotations: map[string]string{"kube-port-forward/policy-hash": policyHash},
		},
		Immutable: &immutable,
		Data:      map[string]string{diagnosticConfigKey: runtimeYAML},
	}
}

func diagnosticOwnerReference(pod *corev1.Pod) metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{
		APIVersion: "v1",
		Kind:       "Pod",
		Name:       pod.Name,
		UID:        pod.UID,
		Controller: &controller,
	}
}

func buildDiagnosticSecret(namespace, name, token string) *corev1.Secret {
	immutable := true
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "kube-port-forward-proxy",
				"app.kubernetes.io/managed-by": "kube-port-forward",
			},
		},
		Immutable: &immutable,
		Type:      corev1.SecretTypeOpaque,
		Data:      map[string][]byte{diagnosticTokenKey: []byte(token)},
	}
}

func buildDiagnosticPod(namespace, name, configMapName, secretName string, profile diagnostic.Profile) *corev1.Pod {
	nonRoot := true
	readOnly := true
	allowPrivilegeEscalation := false
	defaultMode := int32(0o444)
	ttl := int64(profile.TTLDuration().Seconds())
	grace := int64(5)
	pullPolicy := corev1.PullPolicy(profile.PullPolicy)
	pullSecrets := make([]corev1.LocalObjectReference, 0, len(profile.ImagePullSecrets))
	for _, secret := range profile.ImagePullSecrets {
		pullSecrets = append(pullSecrets, corev1.LocalObjectReference{Name: secret})
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "kube-port-forward-proxy",
				"app.kubernetes.io/managed-by": "kube-port-forward",
				"kube-port-forward/session":    strings.TrimPrefix(name, "kpf-diagnostic-"),
			},
		},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken:  ptr(false),
			RestartPolicy:                 corev1.RestartPolicyNever,
			ActiveDeadlineSeconds:         &ttl,
			TerminationGracePeriodSeconds: &grace,
			ImagePullSecrets:              pullSecrets,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   &nonRoot,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:            "proxy",
				Image:           profile.Image,
				ImagePullPolicy: pullPolicy,
				Args: []string{
					"--listen=:" + strconv.Itoa(diagnostic.DefaultProxyPort),
					"--config=/etc/kube-port-forward/" + diagnosticConfigKey,
				},
				Env: []corev1.EnvVar{{
					Name: "KPF_PROXY_TOKEN",
					ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
						Key:                  diagnosticTokenKey,
					}},
				}},
				Ports: []corev1.ContainerPort{{Name: "proxy", ContainerPort: diagnostic.DefaultProxyPort, Protocol: corev1.ProtocolTCP}},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &allowPrivilegeEscalation,
					ReadOnlyRootFilesystem:   &readOnly,
					RunAsNonRoot:             &nonRoot,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
				},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(diagnostic.DefaultProxyPort)}},
					PeriodSeconds: 2,
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/etc/kube-port-forward", ReadOnly: true}},
			}},
			Volumes: []corev1.Volume{{
				Name: "config",
				VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
					DefaultMode:          &defaultMode,
				}},
			}},
		},
	}
}

func tunnelSnapshot(tunnel *diagnosticTunnel) diagnosticTunnelSnapshot {
	snapshot := diagnosticTunnelSnapshot{
		ID: tunnel.id, Name: tunnel.name, Host: tunnel.host, Port: tunnel.port,
		Mode: tunnel.mode, LocalPort: tunnel.localPort, Address: fmt.Sprintf("127.0.0.1:%d", tunnel.localPort),
	}
	if tunnel.mode == "http" || tunnel.mode == "https" {
		snapshot.URL = "http://" + snapshot.Address
	}
	snapshot.TLSSkipVerify = tunnel.tlsSkipVerify
	return snapshot
}

func tlsServerName(host string) string {
	if net.ParseIP(host) != nil {
		return ""
	}
	return host
}

func upstreamHostHeader(host string, port int, mode string) string {
	if (mode == "http" && port == 80) || (mode == "https" && port == 443) {
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func randomID(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func randomToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func ptr[T any](value T) *T { return &value }
