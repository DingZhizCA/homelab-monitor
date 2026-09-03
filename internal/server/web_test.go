package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSPAHandler(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("dashboard"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "app.js"), []byte("javascript"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := NewSPAHandler(directory)
	for _, test := range []struct{ path, content string }{{"/", "dashboard"}, {"/devices/n1", "dashboard"}, {"/app.js", "javascript"}} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.content) {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
}

func TestSPAHandlerRejectsWrites(t *testing.T) {
	handler := NewSPAHandler(t.TempDir())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status=%d content-type=%q", response.Code, response.Header().Get("Content-Type"))
	}
}
