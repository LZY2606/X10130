package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"msgcatalog/internal/server"
	"msgcatalog/internal/store"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:5216", "listen address")
	dataDir := flag.String("data", ".msgcatalog-data", "local data directory")
	flag.Parse()

	st, err := store.Open(*dataDir, time.Now)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()
	rec := st.Recovery()
	if !rec.Healthy {
		log.Printf("WARNING: store recovered in READ-ONLY mode: %v", rec.Notes)
	}
	srv := server.New(st, time.Now)
	log.Printf("message catalog workbench listening on http://%s", *addr)
	if err := http.ListenAndServe(*addr, srv.Routes()); err != nil {
		log.Fatal(err)
	}
}
