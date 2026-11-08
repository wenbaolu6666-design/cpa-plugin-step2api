package cpa

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginabi"
	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
)

func TestResourceRoutesNeverExposeSecrets(t *testing.T) {
	// 1. Create a dummy config.toml containing a fake management secret
	fakeSecret := "test-secret-key-boundary-check-987654321"
	dummyConfig := "management-secret-key = \"" + fakeSecret + "\"" + "\n"
	_ = os.WriteFile("config.toml", []byte(dummyConfig), 0644)
	defer os.Remove("config.toml")

	p := NewPlugin(nil)

	resourcePaths := []string{
		"/accounts",
		"/",
		"/v0/resource/plugins/step2api",
		"/v0/resource/plugins/step2api/accounts",
	}

	for _, resourcePath := range resourcePaths {
		req := pluginapi.ManagementRequest{
			Method: http.MethodGet,
			Path:   resourcePath,
		}
		raw, _ := json.Marshal(req)

		res, err := p.HandleMethod(pluginabi.MethodManagementHandle, raw)
		if err != nil {
			t.Fatalf("path %s: handleManagementHandle returned error: %v", resourcePath, err)
		}

		resp, ok := res.(pluginapi.ManagementResponse)
		if !ok {
			t.Fatalf("path %s: expected ManagementResponse, got %T", resourcePath, res)
		}

		if resp.StatusCode != http.StatusOK {
			t.Errorf("path %s: expected status 200, got %d", resourcePath, resp.StatusCode)
		}

		ct := resp.Headers.Get("Content-Type")
		if !strings.HasPrefix(ct, "text/html") {
			t.Errorf("path %s: expected text/html content-type, got %s", resourcePath, ct)
		}

		bodyStr := string(resp.Body)

		// Verification 1: Fake secret must never be discovered or injected
		if strings.Contains(bodyStr, fakeSecret) {
			t.Fatalf("CRITICAL SECURITY VIOLATION: path %s leaked file-discovered secret: %s", resourcePath, fakeSecret)
		}

		// Verification 2: Server-side injection placeholders or globals must not exist
		forbiddenSubstrings := []string{
			"/*__INJECT_CPA_KEY__*/",
			"window.__CPA_KEY__",
			"__INJECT_CPA_KEY__",
		}
		for _, forbidden := range forbiddenSubstrings {
			if strings.Contains(bodyStr, forbidden) {
				t.Errorf("path %s contains forbidden secret injection marker: %s", resourcePath, forbidden)
			}
		}

		// Verification 3: Static HTML match
		if bodyStr != webUIDashboardHTML {
			t.Errorf("path %s did not serve purely static deployed webUIDashboardHTML", resourcePath)
		}
	}
}

func TestManagementRegistrationRouteBoundaries(t *testing.T) {
	p := NewPlugin(nil)

	res, err := p.HandleMethod(pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatalf("management.register failed: %v", err)
	}

	reg, ok := res.(pluginapi.ManagementRegistrationResponse)
	if !ok {
		t.Fatalf("unexpected registration response type: %T", res)
	}

	// All API routes must be protected under /v0/management/
	for _, route := range reg.Routes {
		if !strings.HasPrefix(route.Path, "/v0/management/") {
			t.Errorf("management route %s is not under /v0/management/ prefix", route.Path)
		}
	}

	// Resource routes must only serve static paths
	for _, resRoute := range reg.Resources {
		if strings.HasPrefix(resRoute.Path, "/v0/management/") {
			t.Errorf("resource route %s illegally uses management prefix", resRoute.Path)
		}
	}
}
