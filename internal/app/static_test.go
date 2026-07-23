package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterStaticRoutesUsesCleanURLs(t *testing.T) {
	staticDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticDir, "voice.html"), []byte("voice page"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "accounts.html"), []byte("accounts page"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "keys.html"), []byte("keys page"), 0o600); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerStaticRoutes(mux, staticDir)

	for path, wantBody := range map[string]string{
		"/voice":    "voice page",
		"/accounts": "accounts page",
		"/keys":     "keys page",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		resp := httptest.NewRecorder()
		mux.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK || strings.TrimSpace(resp.Body.String()) != wantBody {
			t.Fatalf("GET %s: status=%d body=%q", path, resp.Code, resp.Body.String())
		}
	}

	for path, location := range map[string]string{
		"/":              "/voice",
		"/voice.html":    "/voice",
		"/accounts.html": "/accounts",
		"/keys.html":     "/keys",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		resp := httptest.NewRecorder()
		mux.ServeHTTP(resp, req)
		if resp.Code < 300 || resp.Code >= 400 || resp.Header().Get("Location") != location {
			t.Fatalf("GET %s: status=%d location=%q", path, resp.Code, resp.Header().Get("Location"))
		}
	}
}
