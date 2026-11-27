package cpa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
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

// TestAuthLoginPollIgnoresExistingAccounts guards the console badge: an auth
// file from an earlier login must not make a fresh OAuth flow report success.
func TestAuthLoginPollIgnoresExistingAccounts(t *testing.T) {
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	if err := os.Chdir(workDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	if err := os.MkdirAll(filepath.Join("oauth"), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := []byte(`{"type":"step2api","account_id":"old","oasis_token":"eyJhbGciOiJIUzI1NiJ9.eyJkZXZpY2VfaWQiOiJvbGQifQ.sig...refresh"}`)
	if err := os.WriteFile(filepath.Join("oauth", "step2api-old.json"), existing, 0o644); err != nil {
		t.Fatal(err)
	}

	plugin := NewPlugin(nil)
	started, err := plugin.handleAuthLoginStart(nil)
	if err != nil {
		t.Fatal(err)
	}
	startResponse, ok := started.(pluginapi.AuthLoginStartResponse)
	if !ok || startResponse.State == "" {
		t.Fatalf("unexpected start response %#v", started)
	}

	pollRaw, _ := json.Marshal(map[string]any{"state": startResponse.State})
	polled, err := plugin.handleAuthLoginPoll(pollRaw)
	if err != nil {
		t.Fatal(err)
	}
	pollResponse, ok := polled.(pluginapi.AuthLoginPollResponse)
	if !ok {
		t.Fatalf("unexpected poll response %#v", polled)
	}
	if pollResponse.Status != pluginapi.AuthLoginStatusPending {
		t.Fatalf("status = %q, want pending", pollResponse.Status)
	}
}

func TestAccountNoteEndpoint(t *testing.T) {
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	if err := os.Chdir(workDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)
	if err := os.MkdirAll("oauth", 0o755); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join("oauth", "step2api-demo.json")
	original := []byte(`{"type":"step2api","account_id":"demo","name":"demo","oasis_token":"a...b","note":"old","prefix":"keep"}`)
	if err := os.WriteFile(authPath, original, 0o644); err != nil {
		t.Fatal(err)
	}

	plugin := NewPlugin(nil)
	body := []byte(`{"id":"demo","note":"新备注"}`)
	raw, _ := json.Marshal(struct {
		Method string `json:"method"`
		Path   string `json:"path"`
		Body   []byte `json:"body"`
	}{Method: "POST", Path: "/v0/management/step2api/api/accounts/note", Body: body})
	if _, err := plugin.handleManagementHandle(raw); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(updated, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["note"] != "新备注" {
		t.Fatalf("note = %v", fields["note"])
	}
	if fields["prefix"] != "keep" {
		t.Fatalf("unrelated field lost: %v", fields)
	}
}
