package cpa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
)

func TestIsSafeAccountID(t *testing.T) {
	valid := []string{"step-4dfe33e3", "my account", "温保禄", "a.b_c-d@example.com"}
	for _, id := range valid {
		if !isSafeAccountID(id) {
			t.Fatalf("expected %q to be safe", id)
		}
	}
	invalid := []string{"", " ", "..", "x/../../victim", `a\b`, "a:b", "a*b", "a?b", "a|b", "a\x00b"}
	for _, id := range invalid {
		if isSafeAccountID(id) {
			t.Fatalf("expected %q to be rejected", id)
		}
	}
}

func TestSafeAccountPathRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	if _, ok := safeAccountPath(dir, "x/../../victim"); ok {
		t.Fatal("expected traversal id to be rejected")
	}
	path, ok := safeAccountPath(dir, "step-demo")
	if !ok {
		t.Fatal("expected a normal id to be accepted")
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("path escaped the oauth directory: %s", path)
	}
}

func TestDeleteAccountRejectsTraversal(t *testing.T) {
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
	victim := filepath.Join(workDir, "victim.json")
	if err := os.WriteFile(victim, []byte(`{"keep":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	plugin := NewPlugin(nil)
	body, _ := json.Marshal(map[string]string{"id": "x/../../victim"})
	raw, _ := json.Marshal(struct {
		Method string `json:"method"`
		Path   string `json:"path"`
		Body   []byte `json:"body"`
	}{Method: "POST", Path: "/v0/management/step2api/api/accounts/delete", Body: body})
	result, err := plugin.handleManagementHandle(raw)
	if err != nil {
		t.Fatal(err)
	}
	response, ok := result.(pluginapi.ManagementResponse)
	if !ok || response.StatusCode != 400 {
		t.Fatalf("expected HTTP 400, got %#v", result)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("victim file was removed: %v", err)
	}
}

func TestResourceHTMLIsStaticOnly(t *testing.T) {
	html := webUIDashboardHTML
	for _, needle := range []string{"serviceWorker", "getRegistrations", "__CPA_KEY__", "management-secret-key"} {
		if strings.Contains(html, needle) {
			t.Fatalf("resource page must not contain %q", needle)
		}
	}
}
