package plugin

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestRouteModelUsesExplicitPoolProvider(t *testing.T) {
	app := NewApp()
	apiKey := "sk-channel-route"
	hash := hashAPIKey(apiKey)
	app.state.Pools = []PoolConfig{{
		ID: "channel-pool", Name: "Channel", Enabled: true,
		AuthIDs: []string{"channel-auth"}, Providers: []string{"openai-compatible-mimo"}, Models: []string{"mimo-v2"},
	}}
	app.state.KeyBindings = map[string]KeyBinding{hash: {APIKeyHash: hash, PoolID: "channel-pool"}}

	request, err := json.Marshal(ModelRouteRequest{
		RequestedModel:     "mimo-v2",
		Headers:            http.Header{"Authorization": {"Bearer " + apiKey}},
		AvailableProviders: []string{"openai-compatible-mimo", "openai-compatible-other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := app.routeModel(request)
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var response ModelRouteResponse
	if err := json.Unmarshal(envelope.Result, &response); err != nil {
		t.Fatal(err)
	}
	if !response.Handled || response.Target != "openai-compatible-mimo" {
		t.Fatalf("route response = %+v, want explicit provider", response)
	}
}
