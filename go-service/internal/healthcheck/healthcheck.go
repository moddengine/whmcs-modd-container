package healthcheck

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type Checker struct {
	Path         string
	Attempts     int
	InitialDelay time.Duration
	Backoff      time.Duration
}

func (c Checker) Check(ctx context.Context, socket string) error {
	if c.InitialDelay > 0 {
		if err := wait(ctx, c.InitialDelay); err != nil {
			return err
		}
	}
	var last error
	for attempt := 0; attempt < c.Attempts; attempt++ {
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
			},
		}
		client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+c.Path, nil)
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Skip-Redirect", "skip")
		resp, err := client.Do(req)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if readErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			last = fmt.Errorf("status %d: %s", resp.StatusCode, body)
		} else {
			last = err
		}
		transport.CloseIdleConnections()
		if attempt+1 < c.Attempts {
			if err := wait(ctx, time.Duration(attempt+1)*c.Backoff); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("health check failed after %d attempts: %w", c.Attempts, last)
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
