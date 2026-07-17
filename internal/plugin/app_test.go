package plugin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
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

func TestConfigureDefaultStateFileUsesPluginsDirectory(t *testing.T) {
	chdirForTest(t, t.TempDir())
	app := NewApp()

	if err := app.configure(nil); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	if app.stateFile != filepath.Join("plugins", legacyStateFile) {
		t.Fatalf("stateFile = %q, want plugins state file", app.stateFile)
	}
}

func TestConfigureMigratesLegacyStateFile(t *testing.T) {
	chdirForTest(t, t.TempDir())
	legacyRaw := []byte(`{"pools":[{"id":"plus","name":"Plus","auth_ids":["auth-a"],"enabled":true}],"key_bindings":{},"auth_models":{}}`)
	if err := os.WriteFile(legacyStateFile, legacyRaw, 0o600); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}
	app := NewApp()

	if err := app.configure(nil); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	if len(app.state.Pools) != 1 || app.state.Pools[0].ID != "plus" {
		t.Fatalf("pools = %#v, want migrated plus pool", app.state.Pools)
	}
	if _, err := os.Stat(filepath.Join("plugins", legacyStateFile)); err != nil {
		t.Fatalf("migrated state file missing: %v", err)
	}
}

func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
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

func TestSchedulerRoundRobinsSamePriorityCandidates(t *testing.T) {
	app := NewApp()
	apiKey := "sk-round-robin"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "pool-a", Name: "Pool A", Enabled: true, AuthIDs: []string{"auth-a", "auth-b"}, Models: []string{"gpt-test"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "pool-a"}}

	req := SchedulerPickRequest{
		Provider: "openai",
		Model:    "gpt-test",
		Options:  SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "auth-b", Provider: "openai", Priority: 100},
			{ID: "auth-a", Provider: "openai", Priority: 100},
		},
	}
	rawReq, _ := json.Marshal(req)
	want := []string{"auth-a", "auth-b", "auth-a"}
	for index, wantAuthID := range want {
		raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
		if err != nil {
			t.Fatal(err)
		}
		resp := decodeSchedulerResponse(t, raw)
		if !resp.Handled || resp.AuthID != wantAuthID {
			t.Fatalf("response %d = %+v, want %s", index+1, resp, wantAuthID)
		}
	}
}

func TestSchedulerUsesResolvedAuthIDWithoutCandidateTierMetadata(t *testing.T) {
	app := NewApp()
	apiKey := "sk-resolved"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{
		ID:              "002",
		Name:            "plus/team",
		Enabled:         true,
		AccountTypes:    []string{"k12", "team", "plus"},
		ResolvedAuthIDs: []string{"karenmclean0894+go1@gmail.com.json"},
		Models:          []string{"gpt-5.5"},
	}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "002"}}

	req := SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5.5",
		Options:  SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{{
			ID:       "karenmclean0894+go1@gmail.com.json",
			Provider: "codex",
			Priority: 22,
		}},
	}
	rawReq, _ := json.Marshal(req)
	raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeSchedulerResponse(t, raw)
	if !resp.Handled || resp.AuthID != "karenmclean0894+go1@gmail.com.json" {
		t.Fatalf("response = %+v, want resolved k12 account", resp)
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

func TestSchedulerEnforcesPerAccountCodexTierConcurrencyLimit(t *testing.T) {
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
	resp = decodeSchedulerResponse(t, raw)
	if !resp.Handled || resp.AuthID != "codex-plus-b.json" {
		t.Fatalf("second response = %+v, want next account codex-plus-b.json", resp)
	}

	raw, err = app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	pluginErr := decodeEnvelopeError(t, raw)
	if pluginErr.Code != "auth_pool_busy" || pluginErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("third error = %+v, want auth_pool_busy 429", pluginErr)
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

func TestSchedulerUsesPoolTierForExplicitAuthWithoutCandidateTier(t *testing.T) {
	app := NewApp()
	apiKey := "sk-test"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.CodexConcurrencyLimits = map[string]int{"plus": 1, "default": 1}
	app.state.Pools = []PoolConfig{{ID: "pool-plus", Name: "Plus", Enabled: true, AuthIDs: []string{"codex-plus-a.json", "codex-plus-b.json"}, AccountTypes: []string{"plus"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "pool-plus"}}

	req := SchedulerPickRequest{
		Options: SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-plus-a.json", Provider: "codex", Priority: 100},
			{ID: "codex-plus-b.json", Provider: "codex", Priority: 90},
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
	resp = decodeSchedulerResponse(t, raw)
	if !resp.Handled || resp.AuthID != "codex-plus-b.json" {
		t.Fatalf("second response = %+v, want codex-plus-b.json", resp)
	}
}

func TestSchedulerReturnsBusyWhenExplicitPoolTierCandidateIsFull(t *testing.T) {
	app := NewApp()
	apiKey := "sk-test"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.CodexConcurrencyLimits = map[string]int{"plus": 1, "default": 1}
	app.state.Pools = []PoolConfig{{ID: "pool-plus", Name: "Plus", Enabled: true, AuthIDs: []string{"codex-plus-a.json"}, AccountTypes: []string{"plus"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "pool-plus"}}

	req := SchedulerPickRequest{
		Options:    SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{{ID: "codex-plus-a.json", Provider: "codex", Priority: 100}},
	}
	rawReq, _ := json.Marshal(req)
	if raw, err := app.HandleMethod(MethodSchedulerPick, rawReq); err != nil {
		t.Fatal(err)
	} else if resp := decodeSchedulerResponse(t, raw); !resp.Handled || resp.AuthID != "codex-plus-a.json" {
		t.Fatalf("first response = %+v, want codex-plus-a.json", resp)
	}

	raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	pluginErr := decodeEnvelopeError(t, raw)
	if pluginErr.Code != "auth_pool_busy" || pluginErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("second error = %+v, want auth_pool_busy 429", pluginErr)
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

func TestSyncAuthModelsUpdatesResolvedAuthIDs(t *testing.T) {
	app := NewApp()
	app.stateFile = filepath.Join(t.TempDir(), "auth-pool-state.json")
	app.state.Pools = []PoolConfig{{ID: "002", Name: "plus/team", Enabled: true, AccountTypes: []string{"k12", "team", "plus"}}}

	resp := app.syncAuthModels([]byte(`{
		"auth_models":{"karen.json":["gpt-5.5"]},
		"pool_models":{"002":["gpt-5.5"]},
		"pool_resolved_auth_ids":{"002":["karen.json"]}
	}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, resp.Body)
	}
	if !reflect.DeepEqual(app.state.Pools[0].ResolvedAuthIDs, []string{"karen.json"}) {
		t.Fatalf("resolved auth ids = %#v, want karen.json", app.state.Pools[0].ResolvedAuthIDs)
	}
}

func TestSyncResolvedAuthIDsPreservesLastGoodModels(t *testing.T) {
	app := NewApp()
	app.stateFile = filepath.Join(t.TempDir(), "auth-pool-state.json")
	app.state.AuthModels = map[string][]string{"old.json": {"gpt-old"}}
	app.state.Pools = []PoolConfig{{
		ID:              "002",
		Name:            "plus/team",
		Enabled:         true,
		ResolvedAuthIDs: []string{"old.json"},
		Models:          []string{"gpt-old"},
	}}

	resp := app.syncAuthModels([]byte(`{
		"pool_resolved_auth_ids":{"002":["karen.json"]}
	}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, resp.Body)
	}
	if !reflect.DeepEqual(app.state.Pools[0].ResolvedAuthIDs, []string{"karen.json"}) {
		t.Fatalf("resolved auth ids = %#v, want karen.json", app.state.Pools[0].ResolvedAuthIDs)
	}
	if !reflect.DeepEqual(app.state.Pools[0].Models, []string{"gpt-old"}) || !reflect.DeepEqual(app.state.AuthModels["old.json"], []string{"gpt-old"}) {
		t.Fatalf("last-good models were overwritten: pool=%#v auth=%#v", app.state.Pools[0].Models, app.state.AuthModels)
	}
}

func TestSaveAtomicallyReplacesExistingStateFile(t *testing.T) {
	app := NewApp()
	dir := t.TempDir()
	app.stateFile = filepath.Join(dir, "auth-pool-state.json")
	if err := os.WriteFile(app.stateFile, []byte(`{"stale":true}`), 0o600); err != nil {
		t.Fatalf("seed state file: %v", err)
	}
	app.state.Pools = []PoolConfig{{
		ID:              "002",
		Name:            "plus/team",
		Enabled:         true,
		ResolvedAuthIDs: []string{"karen.json"},
	}}

	if err := app.save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(app.stateFile)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if len(state.Pools) != 1 || !reflect.DeepEqual(state.Pools[0].ResolvedAuthIDs, []string{"karen.json"}) {
		t.Fatalf("saved state = %#v, want resolved pool", state)
	}
	tempFiles, err := filepath.Glob(filepath.Join(dir, ".auth-pool-state.json.tmp-*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(tempFiles) != 0 {
		t.Fatalf("temporary files remain: %#v", tempFiles)
	}
}

func TestCloneStateDetachesNestedCollections(t *testing.T) {
	state := State{
		Pools:                  []PoolConfig{{ID: "002", AuthIDs: []string{"manual"}, ResolvedAuthIDs: []string{"resolved"}, Models: []string{"gpt"}}},
		KeyBindings:            map[string]KeyBinding{"hash": {APIKeyHash: "hash", PoolID: "002"}},
		AuthModels:             map[string][]string{"resolved": {"gpt"}},
		ProxyKeyHashes:         []string{"proxy"},
		CodexConcurrencyLimits: map[string]int{"plus": 2},
		ConcurrencySlots:       map[string]ConcurrencySlot{"resolved": {AuthID: "resolved", Count: 1}},
	}
	cloned := cloneState(state)
	state.Pools[0].ResolvedAuthIDs[0] = "changed"
	state.AuthModels["resolved"][0] = "changed"
	state.ProxyKeyHashes[0] = "changed"
	state.CodexConcurrencyLimits["plus"] = 9

	if cloned.Pools[0].ResolvedAuthIDs[0] != "resolved" || cloned.AuthModels["resolved"][0] != "gpt" || cloned.ProxyKeyHashes[0] != "proxy" || cloned.CodexConcurrencyLimits["plus"] != 2 {
		t.Fatalf("clone changed with source: %#v", cloned)
	}
}
