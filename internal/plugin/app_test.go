package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestSchedulerMatchesAllCredentialsForExplicitProviderChannel(t *testing.T) {
	app := NewApp()
	apiKey := "sk-provider-channel"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{
		ID: "provider-pool", Name: "Provider Pool", Enabled: true,
		AuthIDs:   []string{"cpa-provider:openai-compatible-mimo"},
		Providers: []string{"openai-compatible-mimo"},
		Models:    []string{"mimo-v2"},
	}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "provider-pool"}}

	req := SchedulerPickRequest{
		Provider: "openai-compatible-mimo",
		Model:    "mimo-v2",
		Options:  SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "generated-auth-a", Provider: "openai-compatible-mimo", Priority: 100},
			{ID: "generated-auth-b", Provider: "openai-compatible-mimo", Priority: 100},
			{ID: "other-auth", Provider: "openai-compatible-other", Priority: 999},
		},
	}
	rawReq, _ := json.Marshal(req)
	want := []string{"generated-auth-a", "generated-auth-b"}
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

func TestSchedulerFillFirstUsesFirstCandidateUntilConcurrencyLimit(t *testing.T) {
	app := NewApp()
	apiKey := "sk-fill-first"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{
		ID: "pool-a", Name: "Pool A", AuthIDs: []string{"auth-a", "auth-b"}, Models: []string{"gpt-test"},
		SchedulingStrategy: poolSchedulingFillFirst, Enabled: true,
	}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "pool-a"}}
	app.state.CodexConcurrencyLimits = map[string]int{"plus": 2}

	req := SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-test",
		Options:  SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "auth-b", Provider: "codex", Priority: 100, Attributes: map[string]string{"plan_type": "plus"}},
			{ID: "auth-a", Provider: "codex", Priority: 100, Attributes: map[string]string{"plan_type": "plus"}},
		},
	}
	rawReq, _ := json.Marshal(req)
	for index, wantAuthID := range []string{"auth-a", "auth-a", "auth-b"} {
		raw, err := app.pickScheduler(rawReq)
		if err != nil {
			t.Fatal(err)
		}
		resp := decodeSchedulerResponse(t, raw)
		if !resp.Handled || resp.AuthID != wantAuthID {
			t.Fatalf("response %d = %+v, want %s", index+1, resp, wantAuthID)
		}
	}

	app.releaseConcurrencySlot("auth-a")
	raw, err := app.pickScheduler(rawReq)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeSchedulerResponse(t, raw).AuthID; got != "auth-a" {
		t.Fatalf("selected auth after release = %q, want auth-a", got)
	}
}

func TestUpsertPoolNormalizesAndPreservesSchedulingStrategy(t *testing.T) {
	app := NewApp()
	app.stateFile = filepath.Join(t.TempDir(), "state.json")

	response := app.upsertPool([]byte(`{"id":"pool-a","name":"Pool A","scheduling_strategy":"fill_first"}`))
	if response.StatusCode != http.StatusOK || len(app.state.Pools) != 1 || app.state.Pools[0].SchedulingStrategy != poolSchedulingFillFirst {
		t.Fatalf("fill-first upsert response=%d pools=%#v", response.StatusCode, app.state.Pools)
	}
	response = app.upsertPool([]byte(`{"id":"pool-a","name":"Pool A updated"}`))
	if response.StatusCode != http.StatusOK || app.state.Pools[0].SchedulingStrategy != poolSchedulingFillFirst {
		t.Fatalf("strategy was not preserved: response=%d pool=%#v", response.StatusCode, app.state.Pools[0])
	}
	response = app.upsertPool([]byte(`{"id":"pool-b","name":"Pool B"}`))
	if response.StatusCode != http.StatusOK || app.state.Pools[1].SchedulingStrategy != poolSchedulingRoundRobin {
		t.Fatalf("default strategy response=%d pool=%#v", response.StatusCode, app.state.Pools[1])
	}
	response = app.upsertPool([]byte(`{"id":"pool-c","name":"Pool C","scheduling_strategy":"random"}`))
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid strategy status = %d, want 400", response.StatusCode)
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

func TestSchedulerBlocksTrustedProxyHashWithoutBinding(t *testing.T) {
	app := NewApp()
	app.state.ProxyKeyHashes = []string{hashAPIKey("sk-cpa-upstream")}
	headerKeyHash := hashAPIKey("sk-helper-local")
	req := SchedulerPickRequest{
		Options: SchedulerPickOptions{Headers: map[string][]string{
			"Authorization":        {"Bearer sk-cpa-upstream"},
			helperAPIKeyHashHeader: {headerKeyHash},
		}},
		Candidates: []SchedulerAuthCandidate{{ID: "auth-a", Provider: "codex"}},
	}
	rawReq, _ := json.Marshal(req)
	raw, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	pluginErr := decodeEnvelopeError(t, raw)
	if pluginErr.Code != "auth_pool_required" || pluginErr.HTTPStatus != http.StatusForbidden {
		t.Fatalf("error = %+v, want auth_pool_required 403", pluginErr)
	}
	event := app.pluginEventSnapshot(1).Items[0]
	if event.Status != "blocked" || event.Reason != "unbound_api_key" {
		t.Fatalf("event = %#v, want blocked unbound_api_key", event)
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

func TestSchedulerDistributesConcurrentUsersAcrossFullPoolCapacity(t *testing.T) {
	app := NewApp()
	app.state.CodexConcurrencyLimits = map[string]int{"plus": 2, "default": 1}
	app.state.Pools = []PoolConfig{{ID: "pool-plus", Name: "Plus", Enabled: true, AccountTypes: []string{"plus"}, Models: []string{"gpt-test"}}}
	app.state.KeyBindings = map[string]KeyBinding{}

	const accountCount = 4
	const perAccountLimit = 2
	const requestCount = accountCount * perAccountLimit
	candidates := make([]SchedulerAuthCandidate, 0, accountCount)
	for index := 0; index < accountCount; index++ {
		candidates = append(candidates, SchedulerAuthCandidate{
			ID:         fmt.Sprintf("codex-plus-%d.json", index),
			Provider:   "codex",
			Priority:   100,
			Status:     "active",
			Attributes: map[string]string{"plan_type": "plus"},
		})
	}

	type schedulerResult struct {
		raw []byte
		err error
	}
	results := make(chan schedulerResult, requestCount)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := 0; index < requestCount; index++ {
		apiKey := fmt.Sprintf("sk-concurrent-user-%d", index)
		apiKeyHash := hashAPIKey(apiKey)
		app.state.KeyBindings[apiKeyHash] = KeyBinding{APIKeyHash: apiKeyHash, PoolID: "pool-plus", UserID: index + 1}
		rawRequest, errMarshal := json.Marshal(SchedulerPickRequest{
			Model:      "gpt-test",
			Provider:   "codex",
			Options:    SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
			Candidates: candidates,
		})
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			raw, err := app.HandleMethod(MethodSchedulerPick, rawRequest)
			results <- schedulerResult{raw: raw, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	selectedCounts := map[string]int{}
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent scheduler error: %v", result.err)
		}
		response := decodeSchedulerResponse(t, result.raw)
		if !response.Handled || response.AuthID == "" {
			t.Fatalf("concurrent scheduler response = %+v", response)
		}
		selectedCounts[response.AuthID]++
	}
	if len(selectedCounts) != accountCount {
		t.Fatalf("selected accounts = %#v, want all %d accounts", selectedCounts, accountCount)
	}
	for _, candidate := range candidates {
		if selectedCounts[candidate.ID] != perAccountLimit {
			t.Fatalf("selected count for %s = %d, want %d", candidate.ID, selectedCounts[candidate.ID], perAccountLimit)
		}
	}

	extraKey := "sk-concurrent-extra"
	extraHash := hashAPIKey(extraKey)
	app.state.KeyBindings[extraHash] = KeyBinding{APIKeyHash: extraHash, PoolID: "pool-plus"}
	extraRequest, _ := json.Marshal(SchedulerPickRequest{
		Model:      "gpt-test",
		Provider:   "codex",
		Options:    SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + extraKey}}},
		Candidates: candidates,
	})
	raw, err := app.HandleMethod(MethodSchedulerPick, extraRequest)
	if err != nil {
		t.Fatal(err)
	}
	pluginErr := decodeEnvelopeError(t, raw)
	if pluginErr.Code != "auth_pool_busy" || pluginErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("full pool error = %+v, want auth_pool_busy 429", pluginErr)
	}

	for authID, count := range selectedCounts {
		for index := 0; index < count; index++ {
			usageRaw, _ := json.Marshal(UsageRecord{AuthID: authID})
			if _, err := app.HandleMethod(MethodUsageHandle, usageRaw); err != nil {
				t.Fatal(err)
			}
		}
	}
	if slots := app.snapshot().ConcurrencySlots; len(slots) != 0 {
		t.Fatalf("concurrency slots after release = %#v, want empty", slots)
	}
}

func TestSchedulerPrefersLeastLoadedAccountBeforeRoundRobinTieBreak(t *testing.T) {
	app := NewApp()
	apiKey := "sk-least-loaded"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.CodexConcurrencyLimits = map[string]int{"plus": 3, "default": 1}
	app.state.Pools = []PoolConfig{{ID: "pool-plus", Name: "Plus", Enabled: true, AccountTypes: []string{"plus"}, Models: []string{"gpt-test"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "pool-plus"}}
	app.state.ConcurrencySlots = map[string]ConcurrencySlot{
		"codex-plus-a.json": {
			AuthID:    "codex-plus-a.json",
			Tier:      "plus",
			Count:     2,
			StartedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Minute),
		},
	}

	request, _ := json.Marshal(SchedulerPickRequest{
		Model:    "gpt-test",
		Provider: "codex",
		Options:  SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-plus-a.json", Provider: "codex", Priority: 100, Status: "active", Attributes: map[string]string{"plan_type": "plus"}},
			{ID: "codex-plus-b.json", Provider: "codex", Priority: 100, Status: "active", Attributes: map[string]string{"plan_type": "plus"}},
		},
	})
	raw, err := app.HandleMethod(MethodSchedulerPick, request)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeSchedulerResponse(t, raw)
	if response.AuthID != "codex-plus-b.json" {
		t.Fatalf("selected auth = %q, want least-loaded codex-plus-b.json", response.AuthID)
	}
}

func TestAdditionalUsageDoesNotReleasePrimaryConcurrencySlot(t *testing.T) {
	app := NewApp()
	started := time.Now()
	app.state.ConcurrencySlots = map[string]ConcurrencySlot{
		"codex-plus-a.json": {
			AuthID: "codex-plus-a.json", Tier: "plus", Count: 1,
			StartedAt: started, ExpiresAt: started.Add(time.Minute),
		},
	}
	raw, _ := json.Marshal(UsageRecord{AuthID: "codex-plus-a.json", Additional: true})
	if _, err := app.HandleMethod(MethodUsageHandle, raw); err != nil {
		t.Fatal(err)
	}
	if slot := app.state.ConcurrencySlots["codex-plus-a.json"]; slot.Count != 1 {
		t.Fatalf("additional usage released primary slot: %#v", slot)
	}
	raw, _ = json.Marshal(UsageRecord{AuthID: "codex-plus-a.json"})
	if _, err := app.HandleMethod(MethodUsageHandle, raw); err != nil {
		t.Fatal(err)
	}
	if len(app.state.ConcurrencySlots) != 0 {
		t.Fatalf("primary usage did not release slot: %#v", app.state.ConcurrencySlots)
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

func TestSafePluginCallRecoversPanic(t *testing.T) {
	response, err := safePluginCall(func() ([]byte, error) {
		panic("boom")
	})
	if err == nil || response != nil {
		t.Fatalf("safePluginCall response=%q error=%v", response, err)
	}
}

func TestSchedulerSkipsDisabledCandidate(t *testing.T) {
	app := NewApp()
	apiKey := "sk-status-filter"
	hash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "pool", Name: "Pool", AuthIDs: []string{"disabled", "ready"}, Models: []string{"gpt"}, Enabled: true}}
	app.state.KeyBindings = map[string]KeyBinding{hash: {APIKeyHash: hash, PoolID: "pool"}}
	request, _ := json.Marshal(SchedulerPickRequest{
		Model:   "gpt",
		Options: SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "disabled", Priority: 10, Status: "disabled"},
			{ID: "ready", Priority: 1, Status: "ready"},
		},
	})
	raw, err := app.pickScheduler(request)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeSchedulerResponse(t, raw)
	if response.AuthID != "ready" {
		t.Fatalf("selected auth = %q, want ready", response.AuthID)
	}
}

func TestSchedulerUsesPluginPriorityAfterPoolFiltering(t *testing.T) {
	app := NewApp()
	apiKey := "sk-plugin-priority"
	hash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "paid", Name: "Paid", AuthIDs: []string{"k12.json", "plus.json"}, Models: []string{"gpt"}, Enabled: true}}
	app.state.KeyBindings = map[string]KeyBinding{hash: {APIKeyHash: hash, PoolID: "paid"}}
	app.state.AuthTypes = map[string]string{"k12.json": "k12", "plus.json": "plus"}
	app.state.TypePriorities = map[string]int{"k12": 5, "plus": 20}
	request, _ := json.Marshal(SchedulerPickRequest{
		Model:   "gpt",
		Options: SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "k12.json", Provider: "codex", Priority: 100, Status: "active"},
			{ID: "plus.json", Provider: "codex", Priority: 0, Status: "active"},
		},
	})
	raw, err := app.pickScheduler(request)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeSchedulerResponse(t, raw)
	if response.AuthID != "plus.json" {
		t.Fatalf("selected auth = %q, want plus.json", response.AuthID)
	}
}

func TestSchedulerAuthPriorityOverrideWinsTypePriority(t *testing.T) {
	candidate := SchedulerAuthCandidate{ID: "account.json", Priority: 1, Attributes: map[string]string{"plan_type": "plus"}}
	priority := schedulerPriorityForCandidate(candidate, map[string]string{"account.json": "plus"}, map[string]int{"plus": 10}, map[string]int{"account.json": 50})
	if priority != 50 {
		t.Fatalf("scheduler priority = %d, want 50", priority)
	}
}

func TestSchedulerNegativeHostPriorityWinsLogicalPriority(t *testing.T) {
	candidate := SchedulerAuthCandidate{ID: "account.json", Priority: -1, Attributes: map[string]string{"plan_type": "plus"}}
	priority := schedulerPriorityForCandidate(candidate, map[string]string{"account.json": "plus"}, map[string]int{"plus": 10}, map[string]int{"account.json": 50})
	if priority != 50 {
		t.Fatalf("scheduler priority = %d, want override 50 before eligibility filtering", priority)
	}
}

func TestUpdateAuthPrioritiesNormalizesAndRemovesOverrides(t *testing.T) {
	app := NewApp()
	app.stateFile = filepath.Join(t.TempDir(), "state.json")
	response := app.updateAuthPriorities([]byte(`{
		"auth_types":{" account.json ":"Plus"},
		"type_priorities":{"PLUS":12},
		"auth_priority_overrides":{" account.json ":40}
	}`))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, response.Body)
	}
	if app.state.AuthTypes["account_json"] != "plus" || app.state.TypePriorities["plus"] != 12 || app.state.AuthPriorityOverrides["account_json"] != 40 {
		t.Fatalf("priority state = %#v", app.state)
	}
	response = app.updateAuthPriorities([]byte(`{"remove_overrides":["account.json"]}`))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("remove status = %d, body = %s", response.StatusCode, response.Body)
	}
	if _, ok := app.state.AuthPriorityOverrides["account_json"]; ok {
		t.Fatal("manual override was not removed")
	}
}

func TestSchedulerFreePoolIgnoresHigherPriorityOutsideAccount(t *testing.T) {
	app := NewApp()
	apiKey := "priority-test-key-free"
	hash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "free", Name: "Free", AuthIDs: []string{"free-a.json", "free-b.json"}, Models: []string{"codex-auto-review"}, Enabled: true}}
	app.state.KeyBindings = map[string]KeyBinding{hash: {APIKeyHash: hash, PoolID: "free"}}
	app.state.AuthTypes = map[string]string{"free_a_json": "free", "free_b_json": "free", "k12_json": "k12"}
	app.state.TypePriorities = map[string]int{"free": 5, "k12": 50}
	request, _ := json.Marshal(SchedulerPickRequest{
		Model:   "codex-auto-review",
		Options: SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "k12.json", Provider: "codex", Priority: 0, Status: "active"},
			{ID: "free-a.json", Provider: "codex", Priority: 0, Status: "active"},
			{ID: "free-b.json", Provider: "codex", Priority: 0, Status: "active"},
		},
	})
	first, err := app.pickScheduler(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.pickScheduler(request)
	if err != nil {
		t.Fatal(err)
	}
	firstID := decodeSchedulerResponse(t, first).AuthID
	secondID := decodeSchedulerResponse(t, second).AuthID
	if firstID != "free-a.json" || secondID != "free-b.json" {
		t.Fatalf("selected %q then %q, want free pool round robin", firstID, secondID)
	}
}

func TestSchedulerRejectsNegativeHostPriority(t *testing.T) {
	app := NewApp()
	apiKey := "priority-test-key-negative"
	hash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "free", Name: "Free", AuthIDs: []string{"quota.json"}, Models: []string{"gpt"}, Enabled: true}}
	app.state.KeyBindings = map[string]KeyBinding{hash: {APIKeyHash: hash, PoolID: "free"}}
	app.state.AuthTypes = map[string]string{"quota_json": "free"}
	app.state.TypePriorities = map[string]int{"free": 100}
	request, _ := json.Marshal(SchedulerPickRequest{
		Model:      "gpt",
		Options:    SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{{ID: "quota.json", Priority: -1, Status: "active"}},
	})
	raw, err := app.pickScheduler(request)
	if err != nil {
		t.Fatal(err)
	}
	envelopeError := decodeEnvelopeError(t, raw)
	if envelopeError.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("response error = %#v, want 503", envelopeError)
	}
	event := app.pluginEventSnapshot(1).Items[0]
	if event.Reason != "all_candidates_quota_exhausted" || event.PoolMatched != 1 || event.Eligible != 0 {
		t.Fatalf("event = %#v", event)
	}
}

func TestSchedulerNormalizesAuthIDVariants(t *testing.T) {
	app := NewApp()
	apiKey := "priority-test-key-normalized"
	hash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "free", Name: "Free", AuthIDs: []string{"burenbigbie4105@outlook.com.json"}, Models: []string{"gpt"}, Enabled: true}}
	app.state.KeyBindings = map[string]KeyBinding{hash: {APIKeyHash: hash, PoolID: "free"}}
	app.state.AuthTypes = map[string]string{"burenbigbie4105_outlook_com_json": "free"}
	app.state.TypePriorities = map[string]int{"free": 5}
	app.state.AuthPriorityOverrides = map[string]int{"burenbigbie4105_outlook_com_json": 50}
	request, _ := json.Marshal(SchedulerPickRequest{
		Model:      "gpt",
		Options:    SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{{ID: "root_cli_proxy_api_burenbigbie4105_outlook_com_json", Priority: 0, Status: "active"}},
	})
	raw, err := app.pickScheduler(request)
	if err != nil {
		t.Fatal(err)
	}
	if selected := decodeSchedulerResponse(t, raw).AuthID; selected != "root_cli_proxy_api_burenbigbie4105_outlook_com_json" {
		t.Fatalf("selected auth = %q", selected)
	}
	event := app.pluginEventSnapshot(1).Items[0]
	if event.SelectedPriority == nil || *event.SelectedPriority != 50 {
		t.Fatalf("selected priority = %v, want 50", event.SelectedPriority)
	}
}

func TestSchedulerUsesSyncedAccountTypeForDynamicPoolMembership(t *testing.T) {
	app := NewApp()
	apiKey := "priority-test-key-synced-type"
	hash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "free", Name: "Free", AccountTypes: []string{"free"}, Models: []string{"gpt"}, Enabled: true}}
	app.state.KeyBindings = map[string]KeyBinding{hash: {APIKeyHash: hash, PoolID: "free"}}
	app.state.AuthTypes = map[string]string{"account_json": "free"}
	request, _ := json.Marshal(SchedulerPickRequest{
		Model:      "gpt",
		Options:    SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{{ID: "account.json", Provider: "codex", Priority: 0, Status: "active"}},
	})
	raw, err := app.pickScheduler(request)
	if err != nil {
		t.Fatal(err)
	}
	if selected := decodeSchedulerResponse(t, raw).AuthID; selected != "account.json" {
		t.Fatalf("selected auth = %q, want account.json", selected)
	}
}

func TestUpdateAuthPrioritiesRejectsInvalidRange(t *testing.T) {
	app := NewApp()
	app.stateFile = filepath.Join(t.TempDir(), "state.json")
	for _, body := range []string{
		`{"type_priorities":{"free":-1}}`,
		`{"auth_priority_overrides":{"account.json":101}}`,
	} {
		response := app.updateAuthPriorities([]byte(body))
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %s status = %d, want 400", body, response.StatusCode)
		}
	}
}

func TestUpdateAuthPrioritiesSaveFailureDoesNotChangeMemory(t *testing.T) {
	app := NewApp()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	app.stateFile = filepath.Join(blocker, "state.json")
	response := app.updateAuthPriorities([]byte(`{"type_priorities":{"free":5}}`))
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
	if len(app.state.TypePriorities) != 0 {
		t.Fatalf("memory changed after save failure: %#v", app.state.TypePriorities)
	}
}

func TestAuthPrioritiesPersistAcrossRestart(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	app := NewApp()
	app.stateFile = stateFile
	response := app.updateAuthPriorities([]byte(`{
		"auth_types":{"account.json":"plus"},
		"type_priorities":{"plus":15},
		"auth_priority_overrides":{"account.json":50},
		"replace_overrides":true
	}`))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d body=%s", response.StatusCode, response.Body)
	}
	restarted := NewApp()
	restarted.stateFile = stateFile
	if err := restarted.load(); err != nil {
		t.Fatal(err)
	}
	if restarted.state.AuthTypes["account_json"] != "plus" || restarted.state.TypePriorities["plus"] != 15 || restarted.state.AuthPriorityOverrides["account_json"] != 50 {
		t.Fatalf("restarted state = %#v", restarted.state)
	}
}

func TestSchedulerConcurrentPriorityUpdates(t *testing.T) {
	app := NewApp()
	app.stateFile = filepath.Join(t.TempDir(), "state.json")
	apiKey := "priority-test-key-concurrent"
	hash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "pool", Name: "Pool", AuthIDs: []string{"a.json", "b.json"}, Models: []string{"gpt"}, Enabled: true}}
	app.state.KeyBindings = map[string]KeyBinding{hash: {APIKeyHash: hash, PoolID: "pool"}}
	request, _ := json.Marshal(SchedulerPickRequest{
		Model:   "gpt",
		Options: SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "a.json", Priority: 0, Status: "active"},
			{ID: "b.json", Priority: 0, Status: "active"},
		},
	})
	var wait sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 40; iteration++ {
				if _, err := app.pickScheduler(request); err != nil {
					t.Errorf("pick scheduler: %v", err)
					return
				}
			}
		}()
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		for iteration := 0; iteration < 10; iteration++ {
			body := fmt.Sprintf(`{"type_priorities":{"free":%d},"auth_types":{"a.json":"free","b.json":"free"}}`, iteration%6)
			if response := app.updateAuthPriorities([]byte(body)); response.StatusCode != http.StatusOK {
				t.Errorf("priority update status = %d body=%s", response.StatusCode, response.Body)
				return
			}
		}
	}()
	wait.Wait()
}

func TestConfigureRejectsStateFileTraversal(t *testing.T) {
	app := NewApp()
	rawReq, _ := json.Marshal(LifecycleRequest{ConfigYAML: []byte("state_file: ../outside.json\n")})
	if _, err := app.HandleMethod(MethodPluginRegister, rawReq); err == nil || !strings.Contains(err.Error(), "must not traverse") {
		t.Fatalf("configure error = %v, want traversal rejection", err)
	}
}
