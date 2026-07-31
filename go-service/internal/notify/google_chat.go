package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/moddengine/whmcs-container-controller/internal/model"
)

type Notifier interface {
	Send(context.Context, string, bool, model.Service, string, string) error
}

type GoogleChat struct {
	Webhook string
	Client  *http.Client
	Host    string
}

func (g GoogleChat) Send(ctx context.Context, operation string, success bool, service model.Service, requestID, detail string) error {
	if g.Webhook == "" {
		return nil
	}
	result := "success"
	if !success {
		result = "failure"
	}
	text := fmt.Sprintf("%s %s: %s (%s), version %s, deploy %s, host %s, request %s",
		operation, result, service.ID, service.MainDomain, service.Version, service.LiveDeploy, g.Host, requestID)
	if detail != "" {
		text += ": " + truncate(strings.ReplaceAll(detail, g.Webhook, "[redacted]"), 300)
	}
	body, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.Webhook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("google chat returned HTTP %d", resp.StatusCode)
	}
	return nil
}

type Disabled struct{}

func (Disabled) Send(context.Context, string, bool, model.Service, string, string) error { return nil }

func truncate(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
