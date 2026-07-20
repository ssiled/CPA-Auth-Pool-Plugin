package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSchedulerEventsRecordSelectedAccount(t *testing.T) {
	app := NewApp()
	apiKey := "sk-events"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "002", Name: "plus/team", Enabled: true, AuthIDs: []string{"auth-a"}, Models: []string{"gpt-5.5"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "002", UserID: 7, Username: "alice"}}

	req := SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5.5",
		Options:  SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{{
			ID: "auth-a", Provider: "codex", Priority: 22, Status: "active",
		}},
	}
	rawReq, _ := json.Marshal(req)
	if _, err := app.HandleMethod(MethodSchedulerPick, rawReq); err != nil {
		t.Fatal(err)
	}

	snapshot := app.pluginEventSnapshot(10)
	if snapshot.Total != 1 || len(snapshot.Items) != 1 {
		t.Fatalf("snapshot = %#v, want one event", snapshot)
	}
	event := snapshot.Items[0]
	if event.Status != "selected" || event.SelectedAuthID != "auth-a" || event.PoolID != "002" || event.Username != "alice" {
		t.Fatalf("event = %#v, want selected auth-a for pool 002", event)
	}
	if event.CandidateCount != 1 || event.MatchedCount != 1 || event.InputCandidates != 1 || event.PoolMatched != 1 || event.Eligible != 1 || event.HTTPStatus != http.StatusOK {
		t.Fatalf("event counts/status = %#v", event)
	}
	if event.SelectedPriority == nil || *event.SelectedPriority != 22 {
		t.Fatalf("selected priority = %v, want 22", event.SelectedPriority)
	}
}

func TestSchedulerEventsRecordNoEligibleCandidates(t *testing.T) {
	app := NewApp()
	apiKey := "sk-events-blocked"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{ID: "002", Name: "plus/team", Enabled: true, AuthIDs: []string{"missing"}, Models: []string{"gpt-5.5"}}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "002"}}

	req := SchedulerPickRequest{
		Provider:   "codex",
		Model:      "gpt-5.5",
		Options:    SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{{ID: "auth-a", Provider: "codex", Priority: 22}},
	}
	rawReq, _ := json.Marshal(req)
	if _, err := app.HandleMethod(MethodSchedulerPick, rawReq); err != nil {
		t.Fatal(err)
	}

	event := app.pluginEventSnapshot(1).Items[0]
	if event.Status != "blocked" || event.Reason != "pool_no_matching_candidates" || event.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("event = %#v, want no pool matching candidates", event)
	}
	if event.InputCandidates != 1 || event.PoolMatched != 0 || event.Eligible != 0 {
		t.Fatalf("diagnostic counts = %#v", event)
	}
	if len(event.Candidates) != 1 || event.Candidates[0].ID != "auth-a" {
		t.Fatalf("candidate sample = %#v, want auth-a", event.Candidates)
	}
}

func TestUsageEventsRecordFailureAndClear(t *testing.T) {
	app := NewApp()
	app.state.Pools = []PoolConfig{{ID: "002", Name: "plus/team", Enabled: true, ResolvedAuthIDs: []string{"auth-a"}}}
	app.recordUsageEvent(UsageRecord{
		Provider: "codex",
		AuthID:   "auth-a",
		Failed:   true,
		Failure:  UsageFailure{StatusCode: http.StatusTooManyRequests, Body: "rate limited"},
	})

	event := app.pluginEventSnapshot(1).Items[0]
	if event.Phase != "completion" || event.Status != "failed" || event.SelectedAuthID != "auth-a" || event.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("event = %#v, want failed completion", event)
	}
	if cleared := app.clearPluginEvents(); cleared != 1 {
		t.Fatalf("cleared = %d, want 1", cleared)
	}
	if snapshot := app.pluginEventSnapshot(10); snapshot.Total != 0 || len(snapshot.Items) != 0 {
		t.Fatalf("snapshot after clear = %#v", snapshot)
	}
	for _, residual := range app.events[:cap(app.events)] {
		if residual.SelectedAuthID != "" || residual.Reason != "" {
			t.Fatalf("cleared event buffer retained data: %#v", residual)
		}
	}
}

func TestUsageEventsClassifyDetailedFailures(t *testing.T) {
	tests := []struct {
		name            string
		statusCode      int
		body            string
		wantCode        string
		wantMessagePart string
		wantPlan        string
		wantResetsAt    int64
		wantResetsIn    int64
	}{
		{
			name:            "model unsupported",
			statusCode:      http.StatusBadRequest,
			body:            `{"detail":"The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT account."}`,
			wantCode:        "model_not_supported",
			wantMessagePart: "gpt-5.6-sol",
		},
		{
			name:            "proxy network unreachable",
			statusCode:      http.StatusInternalServerError,
			body:            `Post "https://chatgpt.com/backend-api/codex/responses": socks connect tcp 64.188.8.141:1191->chatgpt.com:443: unknown error network unreachable`,
			wantCode:        "proxy_network_unreachable",
			wantMessagePart: "SOCKS proxy",
		},
		{
			name:            "usage limit",
			statusCode:      http.StatusTooManyRequests,
			body:            `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"free","resets_at":1787123950,"eligible_promo":null,"resets_in_seconds":2588673}}`,
			wantCode:        "usage_limit_reached",
			wantMessagePart: "usage limit",
			wantPlan:        "free",
			wantResetsAt:    1787123950,
			wantResetsIn:    2588673,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := NewApp()
			app.state.Pools = []PoolConfig{{ID: "002", Name: "plus/team", Enabled: true, ResolvedAuthIDs: []string{"auth-a"}}}
			app.recordUsageEvent(UsageRecord{
				Provider: "codex",
				Model:    "gpt-5.6-sol",
				AuthID:   "auth-a",
				Failed:   true,
				Failure:  UsageFailure{StatusCode: tt.statusCode, Body: tt.body},
			})

			event := app.pluginEventSnapshot(1).Items[0]
			if event.ErrorCode != tt.wantCode || !strings.Contains(event.ErrorMessage, tt.wantMessagePart) {
				t.Fatalf("classified event = %#v", event)
			}
			if event.ErrorDetail == "" || event.Model != "gpt-5.6-sol" || event.PlanType != tt.wantPlan || event.ResetsAt != tt.wantResetsAt || event.ResetsInSeconds != tt.wantResetsIn {
				t.Fatalf("detailed event = %#v", event)
			}
		})
	}
}

func TestUsageLimitCooldownSwitchesAccountUntilReset(t *testing.T) {
	app := NewApp()
	apiKey := "sk-usage-limit-switch"
	apiKeyHash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{
		ID: "002", Name: "plus/team", Enabled: true, AuthIDs: []string{"auth-a", "auth-b"}, Models: []string{"gpt-5.6-sol"}, SchedulingStrategy: poolSchedulingFillFirst,
	}}
	app.state.KeyBindings = map[string]KeyBinding{apiKeyHash: {APIKeyHash: apiKeyHash, PoolID: "002"}}

	usageRaw, _ := json.Marshal(UsageRecord{
		Provider: "codex", Model: "gpt-5.6-sol", AuthID: "auth-a", Failed: true,
		Failure: UsageFailure{StatusCode: http.StatusTooManyRequests, Body: `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":3600}}`},
	})
	if _, err := app.HandleMethod(MethodUsageHandle, usageRaw); err != nil {
		t.Fatal(err)
	}

	req := SchedulerPickRequest{
		Provider: "codex", Model: "gpt-5.6-sol",
		Options: SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "auth-a", Provider: "codex", Priority: 10, Status: "active"},
			{ID: "auth-b", Provider: "codex", Priority: 10, Status: "active"},
		},
	}
	rawReq, _ := json.Marshal(req)
	rawResponse, err := app.HandleMethod(MethodSchedulerPick, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(rawResponse, &envelope); err != nil {
		t.Fatal(err)
	}
	var response SchedulerPickResponse
	if err := json.Unmarshal(envelope.Result, &response); err != nil {
		t.Fatal(err)
	}
	if response.AuthID != "auth-b" {
		t.Fatalf("selected auth = %q, want auth-b while auth-a is cooling down", response.AuthID)
	}
	cooldown := app.state.FailureCooldowns[normalizeAuthIDKey("auth-a")]
	if cooldown.ErrorCode != "usage_limit_reached" || time.Until(cooldown.Until) < 59*time.Minute {
		t.Fatalf("failure cooldown = %#v", cooldown)
	}

	successRaw, _ := json.Marshal(UsageRecord{Provider: "codex", Model: "gpt-5.6-sol", AuthID: "auth-a"})
	if _, err := app.HandleMethod(MethodUsageHandle, successRaw); err != nil {
		t.Fatal(err)
	}
	if _, exists := app.state.FailureCooldowns[normalizeAuthIDKey("auth-a")]; exists {
		t.Fatal("successful usage did not clear the failure cooldown")
	}
}

func TestUsageLimitCooldownPersistsAcrossReload(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	app := NewApp()
	app.stateFile = stateFile
	usageRaw, _ := json.Marshal(UsageRecord{
		Provider: "codex", AuthID: "auth-a", Failed: true,
		Failure: UsageFailure{StatusCode: http.StatusTooManyRequests, Body: `{"error":{"type":"usage_limit_reached","resets_in_seconds":3600}}`},
	})
	if _, err := app.HandleMethod(MethodUsageHandle, usageRaw); err != nil {
		t.Fatal(err)
	}

	reloaded := NewApp()
	reloaded.stateFile = stateFile
	if err := reloaded.load(); err != nil {
		t.Fatal(err)
	}
	cooldown := reloaded.state.FailureCooldowns[normalizeAuthIDKey("auth-a")]
	if cooldown.ErrorCode != "usage_limit_reached" || !cooldown.Until.After(time.Now()) {
		t.Fatalf("reloaded cooldown = %#v", cooldown)
	}
}

func TestPluginEventBufferIsBounded(t *testing.T) {
	app := NewApp()
	for index := 0; index < pluginEventCapacity+20; index++ {
		app.recordPluginEvent(PluginEvent{Status: "selected"})
	}
	snapshot := app.pluginEventSnapshot(pluginEventCapacity)
	if snapshot.Total != pluginEventCapacity || len(snapshot.Items) != pluginEventCapacity {
		t.Fatalf("snapshot size = %d/%d, want %d", snapshot.Total, len(snapshot.Items), pluginEventCapacity)
	}
	if snapshot.Items[0].ID != pluginEventCapacity+20 {
		t.Fatalf("newest event id = %d, want %d", snapshot.Items[0].ID, pluginEventCapacity+20)
	}
}

func TestPluginEventManagementRoutes(t *testing.T) {
	app := NewApp()
	app.recordPluginEvent(PluginEvent{Status: "selected", SelectedAuthID: "auth-a"})

	getRequest, _ := json.Marshal(ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-auth-pool/events",
		Query:  url.Values{"limit": {"1"}},
	})
	raw, err := app.handleManagement(getRequest)
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var response ManagementResponse
	if err := json.Unmarshal(envelope.Result, &response); err != nil {
		t.Fatalf("decode management response: %v", err)
	}
	var payload pluginEventResponse
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatalf("decode event response: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].SelectedAuthID != "auth-a" {
		t.Fatalf("payload = %#v, want auth-a", payload)
	}

	deleteRequest, _ := json.Marshal(ManagementRequest{Method: http.MethodDelete, Path: "/v0/management/plugins/cpa-auth-pool/events"})
	if _, err := app.handleManagement(deleteRequest); err != nil {
		t.Fatal(err)
	}
	if app.pluginEventSnapshot(10).Total != 0 {
		t.Fatal("events were not cleared")
	}
}

func TestPluginEventReasonRedactsSecrets(t *testing.T) {
	reason := truncatePluginEventReason(`{"error":{"token":"secret-token","message":"Authorization: Bearer abc.def socks5://user:pass@proxy.example.com:1080"}}`)
	if strings.Contains(reason, "secret-token") || strings.Contains(reason, "abc.def") || strings.Contains(reason, "user:pass") {
		t.Fatalf("reason leaked a secret: %s", reason)
	}
	if !strings.Contains(reason, "[REDACTED]") {
		t.Fatalf("reason was not redacted: %s", reason)
	}
}
