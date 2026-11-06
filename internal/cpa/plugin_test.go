package cpa

import (
	"encoding/json"
	"testing"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginabi"
	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
)

func TestPluginLifecycle(t *testing.T) {
	p := NewPlugin(nil)

	// 1. Register
	res, err := p.HandleMethod(pluginabi.MethodPluginRegister, nil)
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	reg, ok := res.(rpcRegistration)
	if !ok || reg.SchemaVersion != pluginabi.SchemaVersion || reg.Metadata.GitHubRepository == "" {
		t.Errorf("unexpected register response: %v", res)
	}

	// 2. Model Static
	res, err = p.HandleMethod(pluginabi.MethodModelStatic, nil)
	if err != nil {
		t.Fatalf("model.static failed: %v", err)
	}
	mResp, ok := res.(pluginapi.ModelResponse)
	if !ok || len(mResp.Models) == 0 {
		t.Errorf("unexpected static models response: %v", res)
	}

	// 3. Model Route
	routeReq := pluginapi.ModelRouteRequest{
		RequestedModel: "step-3.5-flash",
	}
	rawRoute, _ := json.Marshal(routeReq)
	res, err = p.HandleMethod(pluginabi.MethodModelRoute, rawRoute)
	if err != nil {
		t.Fatalf("model.route failed: %v", err)
	}
	rResp, ok := res.(pluginapi.ModelRouteResponse)
	if !ok || !rResp.Handled || rResp.Target != ProviderID {
		t.Errorf("unexpected route decision: %v", res)
	}

	// 4. Auth Identifier
	res, err = p.HandleMethod(pluginabi.MethodAuthIdentifier, nil)
	if err != nil {
		t.Fatalf("auth.identifier failed: %v", err)
	}

	// 5. Auth Parse
	parseReq := pluginapi.AuthParseRequest{
		RawJSON: []byte(`{"oasis_token": "sample_access...sample_refresh", "account_id": "test-user"}`),
	}
	rawParse, _ := json.Marshal(parseReq)
	res, err = p.HandleMethod(pluginabi.MethodAuthParse, rawParse)
	if err != nil {
		t.Fatalf("auth.parse failed: %v", err)
	}
	pResp, ok := res.(pluginapi.AuthParseResponse)
	if !ok || !pResp.Handled || pResp.Auth.ID != "test-user" {
		t.Errorf("unexpected auth.parse result: %v", res)
	}
}
