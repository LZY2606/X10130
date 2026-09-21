package server

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web/index.html web/app.js web/style.css
var webFS embed.FS

//go:embed web/demo/*.json
var demoFS embed.FS

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(webFS, "web/index.html")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) appJS(w http.ResponseWriter, r *http.Request) {
	b, _ := fs.ReadFile(webFS, "web/app.js")
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) css(w http.ResponseWriter, r *http.Request) {
	b, _ := fs.ReadFile(webFS, "web/style.css")
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write(b)
}
