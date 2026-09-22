package diagnostic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConnectThroughProxyRejectsHeaderInjection(t *testing.T) {
	_, err := ConnectThroughProxy(context.Background(), "127.0.0.1:1", "token", "example.internal:80\r\nX-Injected: true")
	if err == nil || err.Error() != "invalid proxy target" {
		t.Fatalf("ConnectThroughProxy() error = %v", err)
	}
}

func TestProxyRequiresAuthentication(t *testing.T) {
	proxy, err := NewProxy(RuntimeConfig{
		MaxConnections: 1, ConnectTimeout: "1s", IdleTimeout: "1m",
		Policy: Policy{DefaultAction: "deny"},
	}, "test-token", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodConnect, "http://proxy.invalid", nil)
	request.Host = "example.internal:443"
	response := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusProxyAuthRequired {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusProxyAuthRequired)
	}
}

func TestProxyAppliesPolicyBeforeConnecting(t *testing.T) {
	proxy, err := NewProxy(RuntimeConfig{
		MaxConnections: 1, ConnectTimeout: "1s", IdleTimeout: "1m",
		Policy: Policy{DefaultAction: "allow"},
	}, "test-token", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodConnect, "http://proxy.invalid", nil)
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Proxy-Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}
