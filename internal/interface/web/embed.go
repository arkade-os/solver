// Package web serves the solverd operator dashboard as embedded static assets.
package web

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"time"
)

//go:embed static/*
var staticFS embed.FS

// Handler returns an http.Handler that serves the dashboard.
//
// Routing:
//   - GET /               -> static/index.html
//   - GET /favicon.svg    -> static/favicon.svg
//   - GET /static/<path>  -> static/<path>
//
// Anything else returns 404 so the caller can chain another handler for /v1/*.
func Handler() http.Handler {
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(static))

	index, err := fs.ReadFile(static, "index.html")
	if err != nil {
		panic(err)
	}
	indexModTime := time.Now()

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", fileServer))
	mux.HandleFunc("GET /favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/favicon.svg"
		fileServer.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", indexModTime, bytes.NewReader(index))
	})
	return mux
}
