package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNewK8sClientInsecureOverridesKubeconfigTLS(t *testing.T) {
	kubeconfig := `apiVersion: v1
kind: Config
current-context: test
clusters:
- name: test-cluster
  cluster:
    server: https://example.invalid
    certificate-authority-data: ` + base64.StdEncoding.EncodeToString([]byte("test CA")) + `
contexts:
- name: test
  context:
    cluster: test-cluster
    user: test-user
users:
- name: test-user
  user:
    token: test-token
`
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	t.Setenv("KUBECONFIG", path)

	client, err := newK8sClient("test", true)
	if err != nil {
		t.Fatalf("newK8sClient() error = %v", err)
	}
	if !client.config.TLSClientConfig.Insecure {
		t.Fatal("newK8sClient() did not disable TLS certificate verification")
	}
	if client.config.TLSClientConfig.CAFile != "" || len(client.config.TLSClientConfig.CAData) != 0 {
		t.Fatal("newK8sClient() retained certificate authority data with insecure TLS enabled")
	}
}

func TestResolveDeployment(t *testing.T) {
	client := &k8sClient{
		clientset: fake.NewSimpleClientset(&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "example",
				Namespace: "test",
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "example"},
				},
			},
		}),
	}

	selector, err := client.resolveDeployment(t.Context(), "test", "example")
	if err != nil {
		t.Fatalf("resolveDeployment() error = %v", err)
	}
	if got := selector.String(); got != "app=example" {
		t.Fatalf("selector = %q, want %q", got, "app=example")
	}
}

func TestListServicesSorted(t *testing.T) {
	client := &k8sClient{
		clientset: fake.NewSimpleClientset(
			&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "zeta", Namespace: "test"}},
			&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "test"}},
		),
	}

	services, err := client.listServices(t.Context(), "test")
	if err != nil {
		t.Fatalf("listServices() error = %v", err)
	}
	if len(services) != 2 || services[0].Name != "alpha" || services[1].Name != "zeta" {
		t.Fatalf("services not sorted: %#v", services)
	}
}
