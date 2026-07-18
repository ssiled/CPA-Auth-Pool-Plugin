package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
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
	reason := truncatePluginEventReason(`{"error":{"token":"secret-token","message":"Authorization: Bearer abc.def"}}`)
	if strings.Contains(reason, "secret-token") || strings.Contains(reason, "abc.def") {
		t.Fatalf("reason leaked a secret: %s", reason)
	}
	if !strings.Contains(reason, "[REDACTED]") {
		t.Fatalf("reason was not redacted: %s", reason)
	}
}
