package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "unhealthy" {
		for {
			time.Sleep(time.Hour)
		}
	}
	path := os.Getenv("ME_SOCKET")
	if path == "" {
		path = filepath.Join("/run/whmcs", os.Getenv("ME_INSTANCE"), "http.sock")
	}
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		panic(err)
	}
	if err := http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})); err != nil {
		panic(err)
	}
}
