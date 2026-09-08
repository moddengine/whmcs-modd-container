package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moddengine/whmcs-container-controller/internal/model"
	"github.com/moddengine/whmcs-container-controller/internal/service"
	"github.com/moddengine/whmcs-container-controller/internal/state"
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

func TestCurrentBulkUpgrade(t *testing.T) {
	root := t.TempDir()
	repo := state.New(filepath.Join(root, "services"), filepath.Join(root, "tombstones"))
	if err := repo.Init(); err != nil {
		t.Fatal(err)
	}
	controller := &API{Manager: &service.Manager{Repo: repo}}
	request := httptest.NewRequest(http.MethodGet, "/v1/bulk-upgrades/current", nil)
	response := httptest.NewRecorder()
	controller.currentBulkUpgrade(response, request)
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body["job"] != nil {
		t.Fatalf("empty bulk upgrade = %#v, %v", body, err)
	}
	if err := repo.PutBulkUpgrade(model.BulkUpgradeJob{ID: "job-1", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	controller.currentBulkUpgrade(response, request)
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	job, ok := body["job"].(map[string]any)
	if !ok || job["id"] != "job-1" {
		t.Fatalf("current bulk upgrade = %#v", body)
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
