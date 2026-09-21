package server

import (
	"net/http"

	"msgcheck/internal/app"
)

// demoFile pairs an embedded demo file with import parameters.
type demoFile struct {
	role     string
	language string
	path     string
}

func (s *Server) demoData(w http.ResponseWriter, r *http.Request) {
	files := []demoFile{
		{"baseline", "en", "web/demo/en.json"},
		{"target", "ja", "web/demo/ja.json"},
		{"target", "fr", "web/demo/fr.json"},
	}
	type step struct {
		Language string `json:"language"`
		Role     string `json:"role"`
		Version  string `json:"version"`
		New      bool   `json:"new_version"`
	}
	var steps []step
	for _, df := range files {
		b, err := demoFS.ReadFile(df.path)
		if err != nil {
			writeErr(w, &app.BadRequestError{Reason: "demo asset missing: " + df.path})
			return
		}
		res, err := s.App.Import(df.role, df.language, df.path, b, "", "")
		if err != nil {
			writeErr(w, err)
			return
		}
		steps = append(steps, step{Language: df.language, Role: df.role, Version: res.Version, New: res.NewVersion})
	}
	writeJSON(w, 200, map[string]any{"imported": steps, "state": s.App.View()})
}
