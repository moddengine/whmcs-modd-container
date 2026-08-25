package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestImagePullStatus(t *testing.T) {
	controller := &API{}
	request := httptest.NewRequest(http.MethodGet, "/v1/image/pull", nil)
	response := httptest.NewRecorder()
	controller.pullImageStatus(response, request)
	var status imagePullStatus
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil || status.Status != "idle" {
		t.Fatalf("initial pull status = %#v, %v", status, err)
	}
	controller.setImagePullStatus(imagePullStatus{Status: "failed", Version: "v2", Error: "denied"})
	response = httptest.NewRecorder()
	controller.pullImageStatus(response, request)
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil || status.Status != "failed" || status.Version != "v2" || status.Error != "denied" {
		t.Fatalf("failed pull status = %#v, %v", status, err)
	}
	controller.setImagePullStatus(imagePullStatus{Status: "pending", Version: "v3"})
	request = httptest.NewRequest(http.MethodPost, "/v1/image/pull", strings.NewReader(`{"version":"v4"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	controller.pullImage(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("concurrent pull returned HTTP %d", response.Code)
	}
}

func TestMonitorTokenScopeTamperingAndExpiry(t *testing.T) {
	secret := "controller-secret"
	token, err := signMonitorToken(monitorClaims{ServiceID: "whmcs-405", Origin: "https://billing.example", Expires: time.Now().Add(time.Hour).Unix()}, secret)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifyMonitorToken(token, secret)
	if err != nil || claims.ServiceID != "whmcs-405" || claims.Origin != "https://billing.example" {
		t.Fatalf("unexpected claims: %#v, %v", claims, err)
	}
	if _, err := verifyMonitorToken(token+"x", secret); err == nil {
		t.Fatal("accepted tampered token")
	}
	expired, _ := signMonitorToken(monitorClaims{ServiceID: "whmcs-405", Origin: "https://billing.example", Expires: time.Now().Add(-time.Second).Unix()}, secret)
	if _, err := verifyMonitorToken(expired, secret); err == nil {
		t.Fatal("accepted expired token")
	}
	if _, err := validOrigin("https://billing.example/path"); err == nil {
		t.Fatal("accepted an origin with a path")
	}
}
