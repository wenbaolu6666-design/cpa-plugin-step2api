package cpa

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPickStepFunCookies(t *testing.T) {
	cookies := []cdpCookie{
		{Name: "_ga", Value: "x", Domain: ".stepfun.com"},
		{Name: "Oasis-Token", Value: "access...refresh", Domain: ".stepfun.com"},
		{Name: "Oasis-Webid", Value: "web-123", Domain: "studio.stepfun.com"},
		{Name: "Oasis-Token", Value: "evil", Domain: ".example.com"},
	}
	token, webid, names := pickStepFunCookies(cookies)
	if token != "access...refresh" {
		t.Fatalf("token = %q", token)
	}
	if webid != "web-123" {
		t.Fatalf("webid = %q", webid)
	}
	if len(names) != 3 {
		t.Fatalf("names = %v", names)
	}
}

func TestPickStepFunCookiesEmpty(t *testing.T) {
	token, webid, names := pickStepFunCookies(nil)
	if token != "" || webid != "" || len(names) != 0 {
		t.Fatalf("unexpected result %q %q %v", token, webid, names)
	}
}

func TestSaveStepFunAccount(t *testing.T) {
	dir := t.TempDir()
	payload, err := saveStepFunAccount(dir, "", "eyJhbGciOiJIUzI1NiJ9.eyJkZXZpY2VfaWQiOiJkZXYtYWJjZGVmZ2hpIn0.sig...refresh", "dev-abcdefghi")
	if err != nil {
		t.Fatal(err)
	}
	if payload.AccountID != "step-dev-abcd" {
		t.Fatalf("account id = %q", payload.AccountID)
	}
	data, err := os.ReadFile(filepath.Join(dir, "step2api-step-dev-abcd.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("empty auth file")
	}
}

func TestSaveStepFunAccountRejectsEmptyToken(t *testing.T) {
	if _, err := saveStepFunAccount(t.TempDir(), "", "  ", ""); err == nil {
		t.Fatal("expected error for empty token")
	}
}
