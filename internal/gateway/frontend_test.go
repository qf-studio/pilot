package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestSPAHandler_ServesIndexHTML(t *testing.T) {
	fs := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>dashboard</html>")},
	}

	handler := &spaHandler{fs: fs, prefix: "/dashboard/"}

	req := httptest.NewRequest("GET", "/dashboard/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("expected text/html content type, got %q", ct)
	}
	if body := w.Body.String(); body != "<html>dashboard</html>" {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestSPAHandler_ServesStaticAssets(t *testing.T) {
	fs := fstest.MapFS{
		"index.html":        &fstest.MapFile{Data: []byte("<html>dashboard</html>")},
		"assets/index.js":   &fstest.MapFile{Data: []byte("console.log('app')")},
		"assets/index.css":  &fstest.MapFile{Data: []byte("body{}")},
	}

	handler := &spaHandler{fs: fs, prefix: "/dashboard/"}

	tests := []struct {
		name       string
		path       string
		wantCache  string
		wantStatus int
	}{
		{"js asset", "/dashboard/assets/index.js", "public, max-age=31536000, immutable", http.StatusOK},
		{"css asset", "/dashboard/assets/index.css", "public, max-age=31536000, immutable", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d", tt.wantStatus, w.Code)
			}
			if cc := w.Header().Get("Cache-Control"); cc != tt.wantCache {
				t.Errorf("expected cache-control %q, got %q", tt.wantCache, cc)
			}
		})
	}
}

func TestSPAHandler_FallbackToIndex(t *testing.T) {
	fs := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>spa</html>")},
	}

	handler := &spaHandler{fs: fs, prefix: "/dashboard/"}

	// Non-existent path should fall back to index.html (SPA routing)
	req := httptest.NewRequest("GET", "/dashboard/settings/profile", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for SPA fallback, got %d", w.Code)
	}
	if body := w.Body.String(); body != "<html>spa</html>" {
		t.Errorf("expected index.html content for SPA fallback, got %q", body)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("expected no-cache for SPA fallback, got %q", cc)
	}
}

func TestServeDashboard_NilFS(t *testing.T) {
	s := NewServer(&Config{Host: "127.0.0.1", Port: 0})
	mux := http.NewServeMux()
	// Should not panic with nil dashboardFS
	s.serveDashboard(mux)
}

func TestServeDashboard_WithFS(t *testing.T) {
	s := NewServer(&Config{Host: "127.0.0.1", Port: 0})

	fs := fstest.MapFS{
		"dashboard_dist/index.html":       &fstest.MapFile{Data: []byte("<html>test</html>")},
		"dashboard_dist/assets/app.js":    &fstest.MapFile{Data: []byte("app()")},
	}
	s.SetDashboardFS(fs)

	mux := http.NewServeMux()
	s.serveDashboard(mux)

	// Test index route
	req := httptest.NewRequest("GET", "/dashboard/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for /dashboard/, got %d", w.Code)
	}
	if body := w.Body.String(); body != "<html>test</html>" {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestServeDashboard_SPARouting(t *testing.T) {
	s := NewServer(&Config{Host: "127.0.0.1", Port: 0})

	fs := fstest.MapFS{
		"dashboard_dist/index.html": &fstest.MapFile{Data: []byte("<html>spa</html>")},
	}
	s.SetDashboardFS(fs)

	mux := http.NewServeMux()
	s.serveDashboard(mux)

	// Deep path should serve index.html
	req := httptest.NewRequest("GET", "/dashboard/tasks/123/details", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for SPA route, got %d", w.Code)
	}
	if body := w.Body.String(); body != "<html>spa</html>" {
		t.Errorf("expected index.html for SPA route, got %q", body)
	}
}

func TestIsStaticAsset(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"assets/index.js", true},
		{"assets/style.css", true},
		{"index.html", false},
		{"favicon.ico", false},
	}

	for _, tt := range tests {
		if got := isStaticAsset(tt.path); got != tt.want {
			t.Errorf("isStaticAsset(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
