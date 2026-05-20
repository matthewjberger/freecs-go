package main

import (
	"flag"
	"log"
	"net/http"
	"strings"
)

func main() {
	dir := flag.String("dir", "docs", "directory to serve")
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	fs := http.FileServer(http.Dir(*dir))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".wasm") {
			w.Header().Set("Content-Type", "application/wasm")
		}
		fs.ServeHTTP(w, r)
	})

	log.Printf("serving %s on http://localhost%s", *dir, *addr)
	if err := http.ListenAndServe(*addr, handler); err != nil {
		log.Fatal(err)
	}
}
