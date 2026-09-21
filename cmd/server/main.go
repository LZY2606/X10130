// Command server runs the message catalog validation workbench.
package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"messagecatalog/internal/service"
	"messagecatalog/internal/store"
)

//go:embed all:web
var webFS embed.FS

func main() {
	addr := flag.String("addr", "127.0.0.1:5216", "listen address")
	dataDir := flag.String("data", ".catalog-data", "local data directory")
	startTime := flag.String("start-time", "", "fixed start time RFC3339 (demo/test)")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	svc := service.New(st)
	if *startTime != "" {
		t, err := time.Parse(time.RFC3339, *startTime)
		if err != nil {
			log.Fatalf("bad start-time: %v", err)
		}
		svc.Clock = service.NewControlledClock(t)
	}
	if st.Degraded() {
		log.Printf("WARNING: storage recovered in degraded/read-only mode")
		for _, ev := range st.RecoveryEvents() {
			log.Printf("recovery: [%s] %s", ev.Scope, ev.Message)
		}
	}
	h := svc.Handlers(webFS)
	fmt.Printf("消息目录校验 workbench listening on http://%s (data=%s)\n", *addr, *dataDir)
	srv := &http.Server{Addr: *addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
