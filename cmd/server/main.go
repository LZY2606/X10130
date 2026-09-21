// Command server runs the local message-catalog validation workbench.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"msgcheck/internal/app"
	"msgcheck/internal/server"
	"msgcheck/internal/store"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:5216", "listen address")
	dataDir := flag.String("data", "", "data directory (default ./.msgcheck-data)")
	flag.Parse()

	dir := *dataDir
	if dir == "" {
		dir = filepath.Join(".", ".msgcheck-data")
	}
	st := store.New(dir)
	a, notes, err := app.New(st)
	if err != nil {
		log.Fatalf("startup: %v", err)
	}
	for _, n := range notes {
		log.Printf("recovery [%s] %s", n.Level, n.Detail)
	}
	srv := server.New(a)
	handler := requestLog(srv.Routes())
	log.Printf("消息目录校验 workbench listening on http://%s (data=%s)", *addr, dir)
	if err := http.ListenAndServe(*addr, handler); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func requestLog(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		h.ServeHTTP(w, r)
	})
}
