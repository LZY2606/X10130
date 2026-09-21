// Command server runs the local message-catalog validation workbench.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"catalogcheck/internal/server"
	"catalogcheck/internal/store"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:5216", "listen address")
	dataDir := flag.String("data", filepath.Join(os.Getenv("HOME"), ".catalogcheck-data"), "local data directory")
	flag.Parse()

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("无法打开本地存储（数据不会被静默重置）: %v", err)
	}
	srv := server.New(st)
	log.Printf("消息目录校验工作台监听 http://%s （数据目录 %s）", *addr, *dataDir)
	if err := http.ListenAndServe(*addr, srv.Mux()); err != nil {
		log.Fatal(err)
	}
}
