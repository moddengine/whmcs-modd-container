package notify

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/moddengine/whmcs-container-controller/internal/model"
)

type failingTransport struct{ webhook string }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("post " + f.webhook + ": unavailable")
}

func TestGoogleChatRedactsWebhookErrors(t *testing.T) {
	webhook := "https://chat.googleapis.com/secret"
	notifier := GoogleChat{Webhook: webhook, Client: &http.Client{Transport: failingTransport{webhook}}}
	err := notifier.Send(context.Background(), "provision", false, model.Service{ID: "whmcs-1"}, "request", "")
	if err == nil || strings.Contains(err.Error(), webhook) {
		t.Fatalf("webhook leaked in error: %v", err)
	}
}
