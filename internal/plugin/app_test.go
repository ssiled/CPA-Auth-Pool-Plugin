package plugin

import (
	"encoding/json"
	"testing"
)

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
