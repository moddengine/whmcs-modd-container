package healthcheck

import (
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestSuccessfulCheckClosesConnection(t *testing.T) {
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "health.sock"))
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{}, 1)
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				select {
				case closed <- struct{}{}:
				default:
				}
			}
		},
	}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })

	if err := (Checker{Path: "/health", Attempts: 1}).Check(t.Context(), listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("successful health-check connection remained open")
	}
}
