package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moddengine/whmcs-container-controller/internal/model"
)

func TestRepositoryRoundTripAndBackup(t *testing.T) {
	root := t.TempDir()
	repo := New(filepath.Join(root, "services"), filepath.Join(root, "tombstones"))
	if err := repo.Init(); err != nil {
		t.Fatal(err)
	}
	service := model.Service{
		ID: "whmcs-123", State: model.Active, MainDomain: "example.com",
		StagingDomain: "example-com.staging.com", PublicIPv4: "203.0.113.10", DNSStatus: "in_sync", DNSSyncedAt: "2026-08-12T00:00:00Z", Version: "v1", LiveDeploy: "blue",
		Package: map[string]string{"plan": "small|Small Hosting Plan"}, TargetPackage: map[string]string{"plan": "large|Large Hosting Plan"},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Deploy: map[string]model.Deploy{"blue": {Socket: "/run/test.sock", Container: "test"}},
	}
	if err := repo.Put(service); err != nil {
		t.Fatal(err)
	}
	service.Version = "v2"
	if err := repo.Put(service); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(service.ID)
	if err != nil || got.Version != "v2" || got.PublicIPv4 != service.PublicIPv4 || got.DNSStatus != "in_sync" || got.DNSSyncedAt == "" || got.Package["plan"] != service.Package["plan"] || got.TargetPackage["plan"] != service.TargetPackage["plan"] {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "services", "whmcs-123.toml.bak")); err != nil {
		t.Fatal("previous service file was not backed up")
	}
}

func TestBulkUpgradeRoundTrip(t *testing.T) {
	root := t.TempDir()
	repo := New(filepath.Join(root, "services"), filepath.Join(root, "tombstones"))
	if err := repo.Init(); err != nil {
		t.Fatal(err)
	}
	job := model.BulkUpgradeJob{
		ID: "request-1", Status: "running", FailureCount: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Items: []model.BulkUpgradeItem{{ServiceID: "whmcs-123", Version: "v2", Status: "queued"}},
	}
	if err := repo.PutBulkUpgrade(job); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetBulkUpgrade()
	if err != nil || got.ID != job.ID || len(got.Items) != 1 || got.Items[0].Version != "v2" {
		t.Fatalf("GetBulkUpgrade() = %#v, %v", got, err)
	}
	services, err := repo.List()
	if err != nil || len(services) != 0 {
		t.Fatalf("bulk upgrade was treated as a service: %#v, %v", services, err)
	}
}
