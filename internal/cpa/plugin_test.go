package cpa

import (
	"encoding/json"
	"testing"

	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginabi"
	"github.com/WenPaul/cpa-plugin-step2api/internal/pluginapi"
)

func TestPluginLifecycle(t *testing.T) {
	p := NewPlugin(nil)

	println("STEP 1")
	// 1. Register
	res, err := p.HandleMethod(pluginabi.MethodPluginRegister, nil)
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	reg, ok := res.(rpcRegistration)
	if !ok || reg.SchemaVersion != pluginabi.SchemaVersion || reg.Metadata.GitHubRepository == "" {
		t.Errorf("unexpected register response: %v", res)
	}
	if !reg.Capabilities.QuotaProvider {
		t.Errorf("expected QuotaProvider capability to be true")
	}

	println("STEP 2")
	// 2. Model Static
	res, err = p.HandleMethod(pluginabi.MethodModelStatic, nil)
	if err != nil {
		t.Fatalf("model.static failed: %v", err)
	}
	mResp, ok := res.(pluginapi.ModelResponse)
	if !ok || len(mResp.Models) == 0 {
		t.Errorf("unexpected static models response: %v", res)
	}

	println("STEP 3")
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

	println("STEP 4")
	// 4. Auth Identifier
	res, err = p.HandleMethod(pluginabi.MethodAuthIdentifier, nil)
	if err != nil {
		t.Fatalf("auth.identifier failed: %v", err)
	}

	println("STEP 5")
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

	println("STEP 6")
	// 6. Quota Identifier & Describe
	res, err = p.HandleMethod(pluginabi.MethodQuotaIdentifier, nil)
	if err != nil {
		t.Fatalf("quota.identifier failed: %v", err)
	}
	qId, ok := res.(rpcIdentifierResponse)
	if !ok || qId.Identifier != ProviderID {
		t.Errorf("unexpected quota identifier response: %v", res)
	}

	res, err = p.HandleMethod(pluginabi.MethodQuotaDescribe, nil)
	if err != nil {
		t.Fatalf("quota.describe failed: %v", err)
	}
	qDesc, ok := res.(pluginapi.QuotaDescribeResponse)
	if !ok || len(qDesc.SupportedProviders) == 0 {
		t.Errorf("unexpected quota describe response: %v", res)
	}

	println("STEP 7")
	// 7. Quota Fetch fallback
	fetchReq := pluginapi.QuotaFetchRequest{
		AuthID:   "test-user",
		Provider: ProviderID,
		Metadata: map[string]any{
			"oasis_token": "sample_access...sample_refresh",
		},
	}
	rawFetch, _ := json.Marshal(fetchReq)
	res, err = p.HandleMethod(pluginabi.MethodQuotaFetch, rawFetch)
	if err != nil {
		t.Fatalf("quota.fetch failed: %v", err)
	}
	qFetch, ok := res.(pluginapi.QuotaFetchResponse)
	if !ok || len(qFetch.Groups) == 0 {
		t.Errorf("unexpected quota fetch response: %v", res)
	}
}
