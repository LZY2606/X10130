package web

import _ "embed"

//go:embed body.html
var indexBody string

//go:embed app.js
var indexJS string

//go:embed shell.html
var indexShell string

// indexHTML is the single-page workbench (no external assets).
var indexHTML = func() string {
	s := indexShell
	for {
		i := indexOf(s, "__BODY__")
		if i < 0 {
			break
		}
		s = s[:i] + indexBody + s[i+len("__BODY__"):]
	}
	for {
		i := indexOf(s, "__JS__")
		if i < 0 {
			break
		}
		s = s[:i] + indexJS + s[i+len("__JS__"):]
	}
	return s
}()

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
