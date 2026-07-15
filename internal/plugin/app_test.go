package plugin

import (
	"encoding/json"
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
	if !registration.Capabilities.Scheduler || !registration.Capabilities.ManagementAPI {
		t.Fatalf("registration capabilities = %+v, want scheduler and management_api", registration.Capabilities)
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
	resp := decodeSchedulerResponse(t, raw)
	if !resp.Handled || resp.AuthID != "" {
		t.Fatalf("response = %+v, want handled empty AuthID", resp)
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
	resp := decodeSchedulerResponse(t, raw)
	if !resp.Handled || resp.AuthID != "" {
		t.Fatalf("response = %+v, want handled empty AuthID", resp)
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
	resp := decodeSchedulerResponse(t, raw)
	if !resp.Handled || resp.AuthID != "" {
		t.Fatalf("response = %+v, want handled empty AuthID", resp)
	}
}
