// Command testserver serves a folder over https for the Windows test workflow: the test manifest and the fake installers.
// It is a test helper, not part of the installer.
package main

import (
	"flag"
	"log"
	"net/http"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8443", "listen address")
	dir := flag.String("dir", ".", "folder to serve")
	cert := flag.String("cert", "cert.pem", "certificate")
	key := flag.String("key", "key.pem", "private key")
	flag.Parse()
	files := http.FileServer(http.Dir(*dir))
	// every request is logged, so the workflow can assert which ones the installer made; the count endpoint just answers
	log.Fatal(http.ListenAndServeTLS(*addr, *cert, *key, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("REQ %s %s", r.Method, r.URL.Path)
		if r.Method == http.MethodPost && r.URL.Path == "/setup/report" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		files.ServeHTTP(w, r)
	})))
}
