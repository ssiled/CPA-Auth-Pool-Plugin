package plugin

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestModelUnsupportedCacheIsScopedToAuthAndModel(t *testing.T) {
	app, apiKey := newBackpressureTestApp(PoolConfig{
		ID: "pool-a", Name: "Pool A", Enabled: true,
		AuthIDs: []string{"auth-a", "auth-b"}, Models: []string{"gpt-5.6-sol", "gpt-5.4"},
		SchedulingStrategy: poolSchedulingFillFirst,
	})

	first := pickBackpressureTestAuth(t, app, apiKey, "gpt-5.6-sol")
	if first != "auth-a" {
		t.Fatalf("first auth = %q, want auth-a", first)
	}
	handleBackpressureTestUsage(t, app, UsageRecord{
		Provider: "codex", Model: "gpt-5.6-sol", AuthID: "auth-a", Failed: true,
		Failure: UsageFailure{StatusCode: http.StatusBadRequest, Body: `{"error":{"message":"account does not support this model"}}`},
	})

	if selected := pickBackpressureTestAuth(t, app, apiKey, "gpt-5.6-sol"); selected != "auth-b" {
		t.Fatalf("same-model auth = %q, want auth-b", selected)
	}
	if selected := pickBackpressureTestAuth(t, app, apiKey, "gpt-5.4"); selected != "auth-a" {
		t.Fatalf("other-model auth = %q, want auth-a", selected)
	}
	if !modelCooldownActive(app.state.ModelCooldowns, "auth-a", "gpt-5.6-sol", time.Now()) {
		t.Fatal("model cooldown was not recorded")
	}
}

func TestNetworkFailureAddsShortAccountCooldown(t *testing.T) {
	app, apiKey := newBackpressureTestApp(PoolConfig{
		ID: "pool-a", Name: "Pool A", Enabled: true,
		AuthIDs: []string{"auth-a", "auth-b"}, Models: []string{"gpt-5.6-sol"},
		SchedulingStrategy: poolSchedulingFillFirst,
	})
	if selected := pickBackpressureTestAuth(t, app, apiKey, "gpt-5.6-sol"); selected != "auth-a" {
		t.Fatalf("first auth = %q", selected)
	}
	handleBackpressureTestUsage(t, app, UsageRecord{
		Provider: "codex", Model: "gpt-5.6-sol", AuthID: "auth-a", Failed: true,
		Failure: UsageFailure{Body: "read tcp: connection reset by peer"},
	})

	cooldown := app.state.FailureCooldowns[normalizeAuthIDKey("auth-a")]
	remaining := time.Until(cooldown.Until)
	if cooldown.ErrorCode != "connection_reset" || remaining < 10*time.Second || remaining > defaultNetworkCooldown+time.Second {
		t.Fatalf("network cooldown = %#v remaining=%s", cooldown, remaining)
	}
	if selected := pickBackpressureTestAuth(t, app, apiKey, "gpt-5.6-sol"); selected != "auth-b" {
		t.Fatalf("cooled account was selected: %q", selected)
	}
}

func TestPoolMaxConcurrencyBlocksAndReleases(t *testing.T) {
	app, apiKey := newBackpressureTestApp(PoolConfig{
		ID: "pool-a", Name: "Pool A", Enabled: true, MaxConcurrency: 1,
		AuthIDs: []string{"auth-a", "auth-b"}, Models: []string{"gpt-5.6-sol"},
		SchedulingStrategy: poolSchedulingRoundRobin,
	})
	selected := pickBackpressureTestAuth(t, app, apiKey, "gpt-5.6-sol")

	rawRequest, _ := json.Marshal(backpressureSchedulerRequest(apiKey, "gpt-5.6-sol"))
	raw, err := app.HandleMethod(MethodSchedulerPick, rawRequest)
	if err != nil {
		t.Fatal(err)
	}
	pluginErr := decodeEnvelopeError(t, raw)
	if pluginErr.Code != "auth_pool_busy" || pluginErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("pool-full error = %#v", pluginErr)
	}

	handleBackpressureTestUsage(t, app, UsageRecord{Provider: "codex", Model: "gpt-5.6-sol", AuthID: selected})
	if slot, exists := app.state.PoolConcurrencySlots["pool-a"]; exists && slot.Count > 0 {
		t.Fatalf("pool slot was not released: %#v", slot)
	}
	if next := pickBackpressureTestAuth(t, app, apiKey, "gpt-5.6-sol"); next == "" {
		t.Fatal("pool remained blocked after completion")
	}
}

func TestModelUnsupportedCachePersistsAcrossReload(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	app := NewApp()
	app.stateFile = stateFile
	handleBackpressureTestUsage(t, app, UsageRecord{
		Provider: "codex", Model: "gpt-5.6-sol", AuthID: "auth-a", Failed: true,
		Failure: UsageFailure{StatusCode: http.StatusBadRequest, Body: `{"error":{"message":"model is not supported"}}`},
	})
	reloaded := NewApp()
	reloaded.stateFile = stateFile
	if err := reloaded.load(); err != nil {
		t.Fatal(err)
	}
	if !modelCooldownActive(reloaded.state.ModelCooldowns, "auth-a", "gpt-5.6-sol", time.Now()) {
		t.Fatal("reloaded model cooldown is missing")
	}
}

func newBackpressureTestApp(pool PoolConfig) (*App, string) {
	app := NewApp()
	apiKey := "sk-backpressure"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{pool}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: pool.ID}}
	return app, apiKey
}

func backpressureSchedulerRequest(apiKey, model string) SchedulerPickRequest {
	return SchedulerPickRequest{
		Provider: "codex", Model: model,
		Options: SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "auth-a", Provider: "codex", Priority: 10, Status: "active"},
			{ID: "auth-b", Provider: "codex", Priority: 10, Status: "active"},
		},
	}
}

func pickBackpressureTestAuth(t *testing.T, app *App, apiKey, model string) string {
	t.Helper()
	rawRequest, _ := json.Marshal(backpressureSchedulerRequest(apiKey, model))
	raw, err := app.HandleMethod(MethodSchedulerPick, rawRequest)
	if err != nil {
		t.Fatal(err)
	}
	response := decodeSchedulerResponse(t, raw)
	if !response.Handled || response.AuthID == "" {
		t.Fatalf("scheduler response = %#v", response)
	}
	return response.AuthID
}

func handleBackpressureTestUsage(t *testing.T, app *App, record UsageRecord) {
	t.Helper()
	raw, _ := json.Marshal(record)
	if _, err := app.HandleMethod(MethodUsageHandle, raw); err != nil {
		t.Fatal(err)
	}
}
