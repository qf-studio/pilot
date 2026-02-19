package gateway

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/alekspetrov/pilot/internal/logging"
)

// SetDashboardFS sets the embedded frontend filesystem for serving the React dashboard.
// The fsys should contain the built frontend files (index.html, assets/, etc.)
// under a "dashboard_dist" subdirectory (from the go:embed directive).
// Must be called before Start().
func (s *Server) SetDashboardFS(fsys fs.FS) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dashboardFS = fsys
}

// serveDashboard registers the /dashboard/ route on the given mux.
// It serves static files from the embedded FS with SPA fallback:
// any path under /dashboard/ that doesn't match a real file serves index.html.
func (s *Server) serveDashboard(mux *http.ServeMux) {
	s.mu.RLock()
	fsys := s.dashboardFS
	s.mu.RUnlock()

	if fsys == nil {
		return
	}

	// Try to access the "dashboard_dist" subdirectory from the embed.
	// The go:embed directive includes the directory name, so we need Sub().
	sub, err := fs.Sub(fsys, "dashboard_dist")
	if err != nil {
		logging.WithComponent("gateway").Warn("dashboard frontend not available", slog.Any("error", err))
		return
	}

	// Verify index.html exists
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		logging.WithComponent("gateway").Warn("dashboard frontend missing index.html", slog.Any("error", err))
		return
	}

	handler := &spaHandler{
		fs:     sub,
		prefix: "/dashboard/",
	}

	mux.Handle("/dashboard/", handler)
	logging.WithComponent("gateway").Info("dashboard frontend registered at /dashboard/")
}

// spaHandler serves static files from an embedded filesystem with SPA fallback.
// For paths that don't match a real file, it serves index.html so client-side
// routing works correctly.
type spaHandler struct {
	fs     fs.FS
	prefix string
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Strip the prefix to get the file path within the FS
	path := strings.TrimPrefix(r.URL.Path, h.prefix)
	if path == "" {
		path = "index.html"
	}

	// Try to open the requested file
	f, err := h.fs.Open(path)
	if err == nil {
		_ = f.Close()
		// File exists — serve it with proper MIME type and cache headers
		if isStaticAsset(path) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.StripPrefix(h.prefix, http.FileServer(http.FS(h.fs))).ServeHTTP(w, r)
		return
	}

	// File not found — SPA fallback: serve index.html
	indexFile, err := fs.ReadFile(h.fs, "index.html")
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(indexFile)
}

// isStaticAsset returns true for paths that are hashed static assets
// (JS, CSS, images in /assets/) which can be cached aggressively.
func isStaticAsset(path string) bool {
	return strings.HasPrefix(path, "assets/")
}
