package main

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mares/kube-port-forward/internal/diagnostic"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestHTTPDiagnosticTunnelRewritesHost(t *testing.T) {
	hosts := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts <- r.Host
		_, _ = io.WriteString(w, "through cluster")
	}))
	defer upstream.Close()
	upstreamAddress := strings.TrimPrefix(upstream.URL, "http://")

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyListener.Close()
	go serveTestConnectProxy(proxyListener, upstreamAddress)

	profile := &diagnostic.Profile{ConnectTimeout: "2s", IdleTimeout: "1m"}
	manager := &diagnosticManager{
		rootCtx: context.Background(), logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		profile: profile, status: "ready", proxyAddress: proxyListener.Addr().String(), token: "token",
		tunnels: make(map[string]*diagnosticTunnel),
	}
	tunnel, err := manager.addTunnel("web", "web.internal", 8080, 0, "http", false)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.closeTunnels()

	response, err := http.Get(tunnel.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if string(body) != "through cluster" {
		t.Fatalf("body = %q", body)
	}
	if host := <-hosts; host != "web.internal:8080" {
		t.Fatalf("Host = %q", host)
	}
}

func TestHTTPSDiagnosticTunnelIsSecureByDefaultAndSupportsExplicitSkipVerify(t *testing.T) {
	hosts := make(chan string, 1)
	authorizations := make(chan string, 1)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts <- r.Host
		authorizations <- r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "secure through cluster")
	}))
	defer upstream.Close()
	upstreamAddress := strings.TrimPrefix(upstream.URL, "https://")

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyListener.Close()
	go serveTestConnectProxy(proxyListener, upstreamAddress)

	profile := &diagnostic.Profile{ConnectTimeout: "2s", IdleTimeout: "1m"}
	manager := &diagnosticManager{
		rootCtx: context.Background(), logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		profile: profile, status: "ready", proxyAddress: proxyListener.Addr().String(), token: "token",
		tunnels: make(map[string]*diagnosticTunnel),
	}
	defer manager.closeTunnels()

	verifiedTunnel, err := manager.addTunnel("verified", "secure.internal", 443, 0, "https", false)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(verifiedTunnel.URL + "/api")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("verified tunnel status = %d, want %d for untrusted test certificate", response.StatusCode, http.StatusBadGateway)
	}

	insecureTunnel, err := manager.addTunnel("explicit-insecure", "secure.internal", 443, 0, "https", true)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, insecureTunnel.URL+"/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer local-client-token")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if string(body) != "secure through cluster" {
		t.Fatalf("body = %q", body)
	}
	if host := <-hosts; host != "secure.internal" {
		t.Fatalf("Host = %q", host)
	}
	if authorization := <-authorizations; authorization != "Bearer local-client-token" {
		t.Fatalf("Authorization was not forwarded")
	}
	if !insecureTunnel.TLSSkipVerify {
		t.Fatal("tunnel snapshot does not report disabled TLS verification")
	}
}

func serveTestConnectProxy(listener net.Listener, upstreamAddress string) {
	for {
		client, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			reader := bufio.NewReader(client)
			request, err := http.ReadRequest(reader)
			if err != nil || request.Method != http.MethodConnect {
				_ = client.Close()
				return
			}
			upstream, err := net.Dial("tcp", upstreamAddress)
			if err != nil {
				_ = client.Close()
				return
			}
			_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n")
			copyBoth(&testBufferedConn{Conn: client, reader: reader}, upstream)
		}()
	}
}

type testBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *testBufferedConn) Read(buffer []byte) (int, error) { return c.reader.Read(buffer) }

func TestBuildDiagnosticPodSecurityAndLifecycle(t *testing.T) {
	profile := diagnostic.Profile{
		Image: "registry.example/proxy:1", PullPolicy: "IfNotPresent", TTL: "2h",
		MaxConnections: 32, ConnectTimeout: "5s", IdleTimeout: "10m",
		Policy: diagnostic.Policy{DefaultAction: "deny"},
	}
	pod := buildDiagnosticPod("test", "kpf-diagnostic-abc", "kpf-diagnostic-abc", "kpf-diagnostic-secret", profile)
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("service account token is mounted")
	}
	if pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 7200 {
		t.Fatalf("ActiveDeadlineSeconds = %v", pod.Spec.ActiveDeadlineSeconds)
	}
	container := pod.Spec.Containers[0]
	if container.SecurityContext == nil || container.SecurityContext.AllowPrivilegeEscalation == nil || *container.SecurityContext.AllowPrivilegeEscalation {
		t.Fatal("privilege escalation is not disabled")
	}
	if container.SecurityContext.ReadOnlyRootFilesystem == nil || !*container.SecurityContext.ReadOnlyRootFilesystem {
		t.Fatal("root filesystem is not read-only")
	}
	if len(container.SecurityContext.Capabilities.Drop) != 1 || container.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("capabilities are not dropped: %#v", container.SecurityContext.Capabilities)
	}
	if container.Env[0].Value != "" || container.Env[0].ValueFrom == nil || container.Env[0].ValueFrom.SecretKeyRef == nil || container.Env[0].ValueFrom.SecretKeyRef.Name != "kpf-diagnostic-secret" {
		t.Fatal("proxy token is not sourced from a Secret")
	}
}

func TestBuildDiagnosticSecretIsImmutable(t *testing.T) {
	secret := buildDiagnosticSecret("test", "gateway", "token")
	if secret.Immutable == nil || !*secret.Immutable {
		t.Fatal("Secret is mutable")
	}
	if string(secret.Data[diagnosticTokenKey]) != "token" {
		t.Fatalf("unexpected Secret data: %#v", secret.Data)
	}
}

func TestBuildDiagnosticConfigMapIsImmutable(t *testing.T) {
	configMap := buildDiagnosticConfigMap("test", "gateway", "policy: {}", "hash")
	if configMap.Immutable == nil || !*configMap.Immutable {
		t.Fatal("ConfigMap is mutable")
	}
	if configMap.Data[diagnosticConfigKey] != "policy: {}" {
		t.Fatalf("unexpected ConfigMap data: %#v", configMap.Data)
	}
}

func TestDiagnosticOwnerReferenceDoesNotRequireFinalizerPermission(t *testing.T) {
	reference := diagnosticOwnerReference(&corev1.Pod{})
	if reference.BlockOwnerDeletion != nil {
		t.Fatal("owner reference unexpectedly requires blockOwnerDeletion permission")
	}
	if reference.APIVersion != "v1" || reference.Kind != "Pod" {
		t.Fatalf("unexpected owner reference: %#v", reference)
	}
}

func TestStopWaitsUntilDiagnosticResourcesAreDeleted(t *testing.T) {
	const namespace = "test"
	const name = "kpf-diagnostic-test"
	clientset := fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}},
	)
	manager := &diagnosticManager{
		client: &k8sClient{clientset: clientset}, namespace: namespace, status: "ready",
		podName: name, configMap: name, secretName: name, tunnels: make(map[string]*diagnosticTunnel),
	}
	if err := manager.stop(); err != nil {
		t.Fatal(err)
	}
	if manager.snapshot().Status != "stopped" {
		t.Fatalf("status = %q", manager.snapshot().Status)
	}
	if _, err := clientset.CoreV1().Pods(namespace).Get(t.Context(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("pod still exists or lookup failed unexpectedly: %v", err)
	}
}
