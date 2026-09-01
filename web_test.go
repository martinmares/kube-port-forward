package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebUIEmbeddedAssets(t *testing.T) {
	handler := (&webServer{}).handler()
	tests := []struct {
		path        string
		contentType string
		contains    string
		cache       string
	}{
		{path: "/", contentType: "text/html", contains: "/static/css/app.css?v=2", cache: "no-cache"},
		{path: "/static/css/tabler.min.css", contentType: "text/css", contains: "Tabler v1.4.0", cache: "public, max-age=3600"},
		{path: "/static/css/app.css?v=2", contentType: "text/css", contains: ".forwards-table", cache: "no-cache"},
		{path: "/static/js/tabler.min.js", contentType: "text/javascript", contains: "Tabler v1.4.0", cache: "public, max-age=3600"},
		{path: "/static/js/app.js?v=2", contentType: "text/javascript", contains: "new EventSource('/events')", cache: "no-cache"},
		{path: "/static/fonts/tabler-icons.woff2", contentType: "font/woff2", cache: "public, max-age=3600"},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, test.contentType) {
				t.Fatalf("Content-Type = %q, want prefix %q", got, test.contentType)
			}
			if got := response.Header().Get("Cache-Control"); got != test.cache {
				t.Fatalf("Cache-Control = %q, want %q", got, test.cache)
			}
			if test.contains != "" && !strings.Contains(response.Body.String(), test.contains) {
				t.Fatalf("response does not contain %q", test.contains)
			}
		})
	}
}

func TestWebUINotFound(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/missing", nil)
	response := httptest.NewRecorder()
	(&webServer{}).handler().ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}
