package api

import (
	"testing"
	"time"
)

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
