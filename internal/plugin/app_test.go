package plugin

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
)

func TestPluginRegistrationUsesConfiguredPluginID(t *testing.T) {
	app := NewApp()
	registration := app.registration()

	if registration.Metadata.Name != PluginID {
		t.Fatalf("registration name = %q, want %q", registration.Metadata.Name, PluginID)
	}
	if registration.Metadata.Version != Version {
		t.Fatalf("registration version = %q, want %q", registration.Metadata.Version, Version)
	}
	if !registration.Capabilities.ModelRouter || !registration.Capabilities.Scheduler || !registration.Capabilities.ResponseInterceptor || !registration.Capabilities.ManagementAPI {
		t.Fatalf("registration capabilities = %+v, want model_router, scheduler, response_interceptor and management_api", registration.Capabilities)
	}
}

func TestConfigureReadsHostConfigYAML(t *testing.T) {
	app := NewApp()
	stateFile := filepath.Join(t.TempDir(), "auth-pool-state.json")
	rawReq, _ := json.Marshal(LifecycleRequest{ConfigYAML: []byte("state_file: " + stateFile + "\n")})

	if err := app.configure(rawReq); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	if app.stateFile != stateFile {
		t.Fatalf("stateFile = %q, want %q", app.stateFile, stateFile)
	}
}

func decodeSchedulerResponse(t *testing.T, raw []byte) SchedulerPickResponse {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("envelope error: %+v", env.Error)
	}
	var resp SchedulerPickResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return resp
}

func decodeEnvelopeError(t *testing.T, raw []byte) *EnvelopeError {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.OK || env.Error == nil {
		t.Fatalf("envelope = %+v, want plugin error", env)
	}
	return env.Error
}

func decodeInterceptResponse(t *testing.T, raw []byte) ResponseInterceptResponse {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("envelope error: %+v", env.Error)
	}
	var resp ResponseInterceptResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return resp
}

func decodeRouteResponse(t *testing.T, raw []byte) ModelRouteResponse {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("envelope error: %+v", env.Error)
	}
	var resp ModelRouteResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return resp
}

func TestSchedulerRestrictsToBoundPool(t *testing.T) {
	app := NewApp()
	apiKey := "sk-test"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "pool-a", Name: "Pool A", Enabled: true, AuthIDs: []string{"auth-b"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "pool-a"}}

	req := SchedulerPickRequest{
		Options: SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "auth-a", Priority: 100},
			{ID: "auth-b", Priority: 1},
		},
	}
	rawReq, _ := json.Marshal(req)
	raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeSchedulerResponse(t, raw)
	if !resp.Handled || resp.AuthID != "auth-b" {
		t.Fatalf("response = %+v, want auth-b", resp)
	}
}

func TestModelRouteLocksCodexPoolToCodexProvider(t *testing.T) {
	app := NewApp()
	headerKeyHash := hashAPIKey("sk-helper-local")
	app.state.ProxyKeyHashes = []string{hashAPIKey("sk-cpa-upstream")}
	app.state.Pools = []PoolConfig{{ID: "plus-team", Name: "Plus Team", Enabled: true, AccountTypes: []string{"plus", "team"}, Models: []string{"gpt-5.5 xhigh"}}}
	app.state.KeyBindings = map[string]KeyBinding{headerKeyHash: {APIKeyHash: headerKeyHash, PoolID: "plus-team"}}

	req := ModelRouteRequest{
		RequestedModel: "gpt-5.5 xhigh",
		Headers: map[string][]string{
			"Authorization":        {"Bearer sk-cpa-upstream"},
			helperAPIKeyHashHeader: {headerKeyHash},
		},
		AvailableProviders: []string{"codex", "openai-compatible-https://vip.j3gb.com"},
	}
	rawReq, _ := json.Marshal(req)
	raw, err := app.HandleMethod(MethodModelRoute, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeRouteResponse(t, raw)
	if !resp.Handled || resp.TargetKind != "provider" || resp.Target != "codex" || resp.TargetModel != "gpt-5.5 xhigh" {
		t.Fatalf("response = %+v, want codex route", resp)
	}
}

func TestModelRouteBlocksModelOutsideBoundPool(t *testing.T) {
	app := NewApp()
	apiKey := "sk-bound"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "codex", Name: "Codex", Enabled: true, AccountTypes: []string{"codex"}, Models: []string{"gpt-5.5 xhigh"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "codex"}}

	req := ModelRouteRequest{
		RequestedModel:     "gpt-5.5 high",
		Headers:            map[string][]string{"Authorization": {"Bearer " + apiKey}},
		AvailableProviders: []string{"codex", "openai-compatible-https://vip.j3gb.com"},
	}
	rawReq, _ := json.Marshal(req)
	raw, err := app.HandleMethod(MethodModelRoute, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeRouteResponse(t, raw)
	if !resp.Handled || resp.Target != "" || resp.TargetModel != "" {
		t.Fatalf("response = %+v, want fail-closed empty route", resp)
	}
}

func TestSchedulerDoesNotTreatOpenAICompatibleAsCodexPool(t *testing.T) {
	app := NewApp()
	apiKey := "sk-codex-pool"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "codex", Name: "Codex", Enabled: true, AccountTypes: []string{"codex"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "codex"}}

	req := SchedulerPickRequest{
		Options: SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "https://vip.j3gb.com", Provider: "openai-compatible-https://vip.j3gb.com", Priority: 100},
		},
	}
	rawReq, _ := json.Marshal(req)
	raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	pluginErr := decodeEnvelopeError(t, raw)
	if pluginErr.Code != "auth_pool_unavailable" || pluginErr.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("error = %+v, want auth_pool_unavailable 503", pluginErr)
	}
}

func TestSchedulerUsesHelperAPIKeyHashHeader(t *testing.T) {
	app := NewApp()
	helperKeyHash := hashAPIKey("sk-helper-local")
	app.state.ProxyKeyHashes = []string{hashAPIKey("sk-cpa-upstream")}
	app.state.Pools = []PoolConfig{{ID: "pool-a", Name: "Pool A", Enabled: true, AuthIDs: []string{"auth-b"}}}
	app.state.KeyBindings = map[string]KeyBinding{helperKeyHash: {APIKeyHash: helperKeyHash, PoolID: "pool-a"}}

	req := SchedulerPickRequest{
		Options: SchedulerPickOptions{Headers: map[string][]string{
			"Authorization":        {"Bearer sk-cpa-upstream"},
			helperAPIKeyHashHeader: {helperKeyHash},
		}},
		Candidates: []SchedulerAuthCandidate{{ID: "auth-a", Priority: 100}, {ID: "auth-b", Priority: 1}},
	}
	rawReq, _ := json.Marshal(req)
	raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeSchedulerResponse(t, raw)
	if !resp.Handled || resp.AuthID != "auth-b" {
		t.Fatalf("response = %+v, want auth-b selected by helper hash", resp)
	}
}

func TestSchedulerIgnoresHelperAPIKeyHashWithoutTrustedProxyKey(t *testing.T) {
	app := NewApp()
	helperKeyHash := hashAPIKey("sk-helper-local")
	app.state.Pools = []PoolConfig{{ID: "pool-a", Name: "Pool A", Enabled: true, AuthIDs: []string{"auth-b"}}}
	app.state.KeyBindings = map[string]KeyBinding{helperKeyHash: {APIKeyHash: helperKeyHash, PoolID: "pool-a"}}

	req := SchedulerPickRequest{
		Options: SchedulerPickOptions{Headers: map[string][]string{
			"Authorization":        {"Bearer sk-cpa-upstream"},
			helperAPIKeyHashHeader: {helperKeyHash},
		}},
		Candidates: []SchedulerAuthCandidate{{ID: "auth-a", Priority: 100}, {ID: "auth-b", Priority: 1}},
	}
	rawReq, _ := json.Marshal(req)
	raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	pluginErr := decodeEnvelopeError(t, raw)
	if pluginErr.Code != "untrusted_proxy_key" || pluginErr.HTTPStatus != http.StatusForbidden {
		t.Fatalf("error = %+v, want untrusted_proxy_key 403", pluginErr)
	}
}

func TestSchedulerMatchesDynamicAccountType(t *testing.T) {
	app := NewApp()
	apiKey := "sk-type"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "pool-plus", Name: "Plus", Enabled: true, AccountTypes: []string{"plus"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "pool-plus"}}

	req := SchedulerPickRequest{
		Options: SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-free.json", Priority: 100, Attributes: map[string]string{"plan_type": "free"}},
			{ID: "codex-plus-new.json", Priority: 1, Attributes: map[string]string{"plan_type": "plus"}},
		},
	}
	rawReq, _ := json.Marshal(req)
	raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeSchedulerResponse(t, raw)
	if !resp.Handled || resp.AuthID != "codex-plus-new.json" {
		t.Fatalf("response = %+v, want dynamically matched plus account", resp)
	}
}

func TestSchedulerRejectsExplicitFreeAuthInPlusPool(t *testing.T) {
	app := NewApp()
	apiKey := "sk-plus-strict"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "pool-plus", Name: "Plus", Enabled: true, AuthIDs: []string{"codex-free.json"}, AccountTypes: []string{"plus/team"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "pool-plus"}}

	req := SchedulerPickRequest{
		Options: SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-free.json", Provider: "codex", Priority: 100, Attributes: map[string]string{"plan_type": "free"}},
		},
	}
	rawReq, _ := json.Marshal(req)
	raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	pluginErr := decodeEnvelopeError(t, raw)
	if pluginErr.Code != "auth_pool_unavailable" || pluginErr.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("error = %+v, want auth_pool_unavailable 503", pluginErr)
	}
}

func TestSchedulerEnforcesCodexTierConcurrencyLimit(t *testing.T) {
	app := NewApp()
	apiKey := "sk-concurrency"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.CodexConcurrencyLimits = map[string]int{"plus": 1, "default": 1}
	app.state.Pools = []PoolConfig{{ID: "pool-plus", Name: "Plus", Enabled: true, AccountTypes: []string{"plus"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "pool-plus"}}

	req := SchedulerPickRequest{
		Options: SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-plus-a.json", Provider: "codex", Priority: 100, Attributes: map[string]string{"plan_type": "plus"}},
			{ID: "codex-plus-b.json", Provider: "codex", Priority: 90, Attributes: map[string]string{"plan_type": "plus"}},
		},
	}
	rawReq, _ := json.Marshal(req)
	raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeSchedulerResponse(t, raw)
	if !resp.Handled || resp.AuthID != "codex-plus-a.json" {
		t.Fatalf("first response = %+v, want codex-plus-a.json", resp)
	}

	raw, err = app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	pluginErr := decodeEnvelopeError(t, raw)
	if pluginErr.Code != "auth_pool_unavailable" || pluginErr.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("second error = %+v, want auth_pool_unavailable 503", pluginErr)
	}

	usageRaw, _ := json.Marshal(UsageRecord{AuthID: "codex-plus-a.json"})
	if _, err := app.HandleMethod(MethodUsageHandle, usageRaw); err != nil {
		t.Fatal(err)
	}
	raw, err = app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	resp = decodeSchedulerResponse(t, raw)
	if !resp.Handled || resp.AuthID != "codex-plus-a.json" {
		t.Fatalf("after release response = %+v, want codex-plus-a.json", resp)
	}
}
func TestSchedulerReservesConcurrencyAfterPrioritySelection(t *testing.T) {
	app := NewApp()
	apiKey := "sk-priority-concurrency"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.CodexConcurrencyLimits = map[string]int{"plus": 1, "default": 1}
	app.state.Pools = []PoolConfig{{ID: "pool-plus", Name: "Plus", Enabled: true, AccountTypes: []string{"plus"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "pool-plus"}}

	req := SchedulerPickRequest{
		Options: SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-plus-low.json", Provider: "codex", Priority: 1, Attributes: map[string]string{"plan_type": "plus"}},
			{ID: "codex-plus-high.json", Provider: "codex", Priority: 100, Attributes: map[string]string{"plan_type": "plus"}},
		},
	}
	rawReq, _ := json.Marshal(req)
	raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeSchedulerResponse(t, raw)
	if !resp.Handled || resp.AuthID != "codex-plus-high.json" {
		t.Fatalf("response = %+v, want high priority auth", resp)
	}
}

func TestSchedulerMatchesGeminiAndGrokAccountTypes(t *testing.T) {
	tests := []struct {
		name       string
		poolType   string
		candidate  SchedulerAuthCandidate
		wantAuthID string
	}{
		{name: "gemini provider", poolType: "gemini", candidate: SchedulerAuthCandidate{ID: "google-oauth.json", Provider: "google", Priority: 1}, wantAuthID: "google-oauth.json"},
		{name: "grok id", poolType: "grok", candidate: SchedulerAuthCandidate{ID: "xai-grok-key.json", Provider: "openai-compatible", Priority: 1}, wantAuthID: "xai-grok-key.json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := NewApp()
			apiKey := "sk-" + test.poolType
			apiKeyHash := hashAPIKey(apiKey)
			app.state.Pools = []PoolConfig{{ID: "pool-" + test.poolType, Name: test.poolType, Enabled: true, AccountTypes: []string{test.poolType}}}
			app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "pool-" + test.poolType}}
			req := SchedulerPickRequest{
				Options: SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
				Candidates: []SchedulerAuthCandidate{
					{ID: "codex-free.json", Provider: "codex", Priority: 100},
					test.candidate,
				},
			}
			rawReq, _ := json.Marshal(req)
			raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
			if err != nil {
				t.Fatal(err)
			}
			resp := decodeSchedulerResponse(t, raw)
			if !resp.Handled || resp.AuthID != test.wantAuthID {
				t.Fatalf("response = %+v, want %s", resp, test.wantAuthID)
			}
		})
	}
}

func TestSchedulerDoesNotFallbackWhenPoolEmpty(t *testing.T) {
	app := NewApp()
	apiKey := "sk-test"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "pool-a", Name: "Pool A", Enabled: true, AuthIDs: []string{"missing-auth"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "pool-a"}}

	req := SchedulerPickRequest{
		Options:    SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{{ID: "auth-a", Priority: 100}},
	}
	rawReq, _ := json.Marshal(req)
	raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	pluginErr := decodeEnvelopeError(t, raw)
	if pluginErr.Code != "auth_pool_unavailable" || pluginErr.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("error = %+v, want auth_pool_unavailable 503", pluginErr)
	}
}

func TestSchedulerBlocksModelsOutsideBoundPool(t *testing.T) {
	app := NewApp()
	apiKey := "sk-forbidden-model"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "pool-a", Name: "Pool A", Enabled: true, AuthIDs: []string{"auth-a"}, Models: []string{"gpt-a"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "pool-a"}}

	req := SchedulerPickRequest{
		Model:      "gpt-b",
		Options:    SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{{ID: "auth-a", Priority: 100}},
	}
	rawReq, _ := json.Marshal(req)
	raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	pluginErr := decodeEnvelopeError(t, raw)
	if pluginErr.Code != "model_not_allowed" || pluginErr.HTTPStatus != http.StatusForbidden {
		t.Fatalf("error = %+v, want model_not_allowed 403", pluginErr)
	}
}

func TestSchedulerDoesNotFallbackWhenPoolMissing(t *testing.T) {
	app := NewApp()
	apiKey := "sk-missing-pool"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "missing-pool"}}

	req := SchedulerPickRequest{
		Options:    SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{{ID: "auth-a", Priority: 100}},
	}
	rawReq, _ := json.Marshal(req)
	raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	pluginErr := decodeEnvelopeError(t, raw)
	if pluginErr.Code != "auth_pool_unavailable" || pluginErr.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("error = %+v, want auth_pool_unavailable 503", pluginErr)
	}
}

func TestSchedulerDoesNotFallbackWhenPoolDisabled(t *testing.T) {
	app := NewApp()
	apiKey := "sk-disabled-pool"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "pool-a", Name: "Pool A", Enabled: false, AuthIDs: []string{"auth-a"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "pool-a"}}

	req := SchedulerPickRequest{
		Options:    SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{{ID: "auth-a", Priority: 100}},
	}
	rawReq, _ := json.Marshal(req)
	raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	pluginErr := decodeEnvelopeError(t, raw)
	if pluginErr.Code != "auth_pool_unavailable" || pluginErr.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("error = %+v, want auth_pool_unavailable 503", pluginErr)
	}
}

func TestModelsEndpointFiltersToBoundPoolModels(t *testing.T) {
	app := NewApp()
	apiKey := "sk-models"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "pool-a", Name: "Pool A", Enabled: true, AuthIDs: []string{"auth-a"}, Models: []string{"gpt-a"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "pool-a"}}

	body := []byte(`{"object":"list","data":[{"id":"gpt-a","object":"model"},{"id":"gpt-b","object":"model"}]}`)
	req, _ := json.Marshal(ResponseInterceptRequest{
		Path:           "/v1/models",
		StatusCode:     200,
		RequestHeaders: map[string][]string{"Authorization": {"Bearer " + apiKey}},
		Body:           body,
	})
	raw, err := app.HandleMethod(MethodResponseIntercept, req)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeInterceptResponse(t, raw)
	if len(resp.Body) == 0 {
		t.Fatalf("response body is empty, want filtered model list")
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Data[0]["id"] != "gpt-a" {
		t.Fatalf("filtered payload = %s, want only gpt-a", resp.Body)
	}
}

func TestModelsEndpointUsesHelperAPIKeyHashHeader(t *testing.T) {
	app := NewApp()
	helperKeyHash := hashAPIKey("sk-helper-local")
	app.state.ProxyKeyHashes = []string{hashAPIKey("sk-cpa-upstream")}
	app.state.Pools = []PoolConfig{{ID: "pool-a", Name: "Pool A", Enabled: true, AuthIDs: []string{"auth-a"}, Models: []string{"gpt-a"}}}
	app.state.KeyBindings = map[string]KeyBinding{helperKeyHash: {APIKeyHash: helperKeyHash, PoolID: "pool-a"}}

	body := []byte(`{"object":"list","data":[{"id":"gpt-a","object":"model"},{"id":"gpt-b","object":"model"}]}`)
	req, _ := json.Marshal(ResponseInterceptRequest{
		Path:       "/v1/models",
		StatusCode: 200,
		RequestHeaders: map[string][]string{
			"Authorization":        {"Bearer sk-cpa-upstream"},
			helperAPIKeyHashHeader: {helperKeyHash},
		},
		Body: body,
	})
	raw, err := app.HandleMethod(MethodResponseIntercept, req)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeInterceptResponse(t, raw)
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Data[0]["id"] != "gpt-a" {
		t.Fatalf("filtered payload = %s, want only gpt-a selected by helper hash", resp.Body)
	}
}

func TestModelsEndpointFailsClosedWhenPoolHasNoModels(t *testing.T) {
	app := NewApp()
	apiKey := "sk-empty-models"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "pool-empty", Name: "Pool Empty", Enabled: true, AuthIDs: []string{"auth-a"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "pool-empty"}}

	body := []byte(`{"object":"list","data":[{"id":"gpt-a","object":"model"},{"id":"gpt-b","object":"model"}]}`)
	req, _ := json.Marshal(ResponseInterceptRequest{
		Path:           "/v1/models",
		StatusCode:     200,
		RequestHeaders: map[string][]string{"Authorization": {"Bearer " + apiKey}},
		Body:           body,
	})
	raw, err := app.HandleMethod(MethodResponseIntercept, req)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeInterceptResponse(t, raw)
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 0 {
		t.Fatalf("filtered payload = %s, want no available models", resp.Body)
	}
}

func TestSyncAuthModelsAcceptsPoolModels(t *testing.T) {
	app := NewApp()
	app.stateFile = filepath.Join(t.TempDir(), "auth-pool-state.json")
	app.state.Pools = []PoolConfig{{ID: "pool-type", Name: "Pool Type", Enabled: true, AccountTypes: []string{"plus"}}}

	resp := app.syncAuthModels([]byte(`{
		"auth_models":{"auth-a":["gpt-a"]},
		"pool_models":{"pool-type":["gpt-type"]}
	}`))
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, resp.Body)
	}
	if len(app.state.Pools[0].Models) != 1 || app.state.Pools[0].Models[0] != "gpt-type" {
		t.Fatalf("pool models = %#v, want gpt-type", app.state.Pools[0].Models)
	}
}
