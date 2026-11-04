package model

import (
	"encoding/json"
	"testing"
)

func TestMatomoRequestID(t *testing.T) {
	for _, body := range []string{`{}`, `{"matomo_id":0}`, `{"matomo_id":4294967295}`} {
		var provision ProvisionRequest
		var resume ResumeRequest
		if err := json.Unmarshal([]byte(body), &provision); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(body), &resume); err != nil {
			t.Fatal(err)
		}
		if body == `{}` {
			if provision.MatomoID != nil || resume.MatomoID != nil {
				t.Fatal("omitted ID became an explicit clear")
			}
		} else if provision.MatomoID == nil || resume.MatomoID == nil || *provision.MatomoID != *resume.MatomoID {
			t.Fatal("explicit ID was lost")
		}
	}
	for _, value := range []string{`-1`, `4294967296`, `1.5`, `"123"`, `true`} {
		body := []byte(`{"matomo_id":` + value + `}`)
		if json.Unmarshal(body, &ProvisionRequest{}) == nil || json.Unmarshal(body, &ResumeRequest{}) == nil {
			t.Fatalf("accepted invalid matomo_id %s", value)
		}
	}
}
