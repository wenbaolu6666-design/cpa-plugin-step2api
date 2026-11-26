package cpa

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
)

// TestBrowserLoginSmoke launches a real isolated browser window. It only runs
// when STEP2API_BROWSER_LOGIN_SMOKE=1 so CI and headless machines skip it.
func TestBrowserLoginSmoke(t *testing.T) {
	if os.Getenv("STEP2API_BROWSER_LOGIN_SMOKE") != "1" {
		t.Skip("set STEP2API_BROWSER_LOGIN_SMOKE=1 to launch a real browser window")
	}
	plugin := NewPlugin(nil)
	session, err := plugin.StartBrowserLogin()
	if err != nil {
		t.Fatalf("start browser login: %v", err)
	}
	if session.Status != browserLoginWaiting {
		t.Fatalf("status = %q", session.Status)
	}
	profileDir := ""
	for i := 0; i < 40; i++ {
		snapshot := plugin.browserLoginSnapshot()
		if snapshot == nil {
			break
		}
		if snapshot.ProfileDir != "" {
			profileDir = snapshot.ProfileDir
		}
		if len(snapshot.Cookies) > 0 || snapshot.Status == browserLoginError {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	snapshot := plugin.browserLoginSnapshot()
	t.Logf("snapshot: status=%q cookies=%v message=%q", snapshot.Status, snapshot.Cookies, snapshot.Message)
	if len(snapshot.Cookies) == 0 {
		t.Fatalf("expected to read at least one stepfun cookie, got %v", snapshot.Cookies)
	}
	for _, name := range snapshot.Cookies {
		if strings.TrimSpace(name) == "" {
			t.Fatalf("empty cookie name in %v", snapshot.Cookies)
		}
	}

	cancelled := plugin.CancelBrowserLogin()
	if cancelled.Status != browserLoginCancelled {
		t.Fatalf("cancel status = %q", cancelled.Status)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		current := plugin.browserLoginSnapshot()
		if current != nil && (current.Status == browserLoginCancelled || current.Status == browserLoginError) {
			if profileDir != "" {
				if _, statErr := os.Stat(profileDir); os.IsNotExist(statErr) {
					t.Logf("profile dir removed: %s", profileDir)
					return
				}
			} else {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if profileDir != "" {
		t.Fatalf("temporary profile was not removed: %s", profileDir)
	}
}

// TestBrowserLoginManagementRoutes exercises the HTTP routes used by the panel
// button. Guarded by the same environment variable because it opens a window.
func TestBrowserLoginManagementRoutes(t *testing.T) {
	if os.Getenv("STEP2API_BROWSER_LOGIN_SMOKE") != "1" {
		t.Skip("set STEP2API_BROWSER_LOGIN_SMOKE=1 to launch a real browser window")
	}
	plugin := NewPlugin(nil)
	call := func(method, path string) map[string]any {
		raw, _ := json.Marshal(struct {
			Method string `json:"method"`
			Path   string `json:"path"`
			Body   []byte `json:"body"`
		}{Method: method, Path: path, Body: []byte("{}")})
		result, err := plugin.handleManagementHandle(raw)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		response, ok := result.(pluginapi.ManagementResponse)
		if !ok {
			t.Fatalf("%s %s: unexpected response type %T", method, path, result)
		}
		var decoded map[string]any
		if err := json.Unmarshal(response.Body, &decoded); err != nil {
			t.Fatalf("%s %s: %v (%s)", method, path, err, string(response.Body))
		}
		return decoded
	}

	started := call("POST", "/v0/management/step2api/api/accounts/browser-login")
	if started["ok"] != true {
		t.Fatalf("start failed: %v", started)
	}
	session, _ := started["session"].(map[string]any)
	if session == nil || session["status"] != browserLoginWaiting {
		t.Fatalf("unexpected session: %v", started)
	}
	status := call("GET", "/v0/management/step2api/api/accounts/browser-login")
	if status["session"] == nil {
		t.Fatalf("missing session in status: %v", status)
	}
	cancelled := call("POST", "/v0/management/step2api/api/accounts/browser-login/cancel")
	session, _ = cancelled["session"].(map[string]any)
	if session == nil || session["status"] != browserLoginCancelled {
		t.Fatalf("cancel failed: %v", cancelled)
	}
}
