package app

import (
	"net/http"
	"os"
	"path/filepath"
)

// registerPublicStaticAssets serves CSS/JS/asset files under /static/ without
// authentication so the login page can load the shared design system.
func registerPublicStaticAssets(mux *http.ServeMux, staticDir string) {
	if info, err := os.Stat(staticDir); err == nil && info.IsDir() {
		fileServer := http.FileServer(http.Dir(staticDir))
		mux.Handle("GET /static/", http.StripPrefix("/static/", fileServer))
	}
}

func registerStaticRoutes(mux *http.ServeMux, staticDir string) {
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/voice", http.StatusFound)
	})
	mux.HandleFunc("GET /voice", func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, joinStatic(staticDir, "voice.html"))
	})
	mux.HandleFunc("GET /accounts", func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, joinStatic(staticDir, "accounts.html"))
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, joinStatic(staticDir, "keys.html"))
	})
	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, joinStatic(staticDir, "sessions.html"))
	})
	// Keep the former file-suffixed URLs as canonical redirects for bookmarks
	// and external links created before clean routes were introduced.
	mux.HandleFunc("GET /voice.html", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/voice", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /accounts.html", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/accounts", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /keys.html", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/keys", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /sessions.html", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/sessions", http.StatusMovedPermanently)
	})
}

func joinStatic(staticDir, name string) string {
	return filepath.Join(staticDir, name)
}

func serveFile(w http.ResponseWriter, r *http.Request, path string) {
	if _, err := os.Stat(path); err != nil {
		writeJSONError(w, "resource missing")
		return
	}
	http.ServeFile(w, r, path)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "microphone=(self), camera=()")
		next.ServeHTTP(w, r)
	})
}

func writeJSONError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}
