package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	path := filepath.Join("/run/moddengine", os.Getenv("ME_INSTANCE"), "http.sock")
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
