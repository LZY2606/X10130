// Command server runs the local message-catalog validation workbench.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"messagecatalog/internal/service"
	"messagecatalog/internal/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:5216", "listen address")
	dataDir := flag.String("data", envOr("CATALOG_DATA", filepath.Join(".", "catalog-data")), "local data directory")
	flag.Parse()

	svc, err := service.New(*dataDir)
	if err != nil {
		log.Fatalf("failed to open local storage: %v", err)
	}
	if deg, note := svc.Degraded(); deg {
		log.Printf("WARNING: integrity check failed; running read-only: %s", note)
	}
	srv := web.New(svc)
	log.Printf("消息目录校验 listening on http://%s (data=%s)", *addr, *dataDir)
	if err := http.ListenAndServe(*addr, srv); err != nil {
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
