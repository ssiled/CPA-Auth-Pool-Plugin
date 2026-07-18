package plugin

import (
	"encoding/json"
	"fmt"
	"testing"
)

func BenchmarkSchedulerPick1000Candidates(b *testing.B) {
	app := NewApp()
	apiKey := "sk-benchmark"
	hash := hashAPIKey(apiKey)
	ids := make([]string, 1000)
	candidates := make([]SchedulerAuthCandidate, 1000)
	for index := range candidates {
		ids[index] = fmt.Sprintf("auth-%04d.json", index)
		candidates[index] = SchedulerAuthCandidate{
			ID:       ids[index],
			Provider: "codex",
			Priority: index % 20,
			Status:   "active",
		}
	}
	app.state.Pools = []PoolConfig{{ID: "benchmark", Name: "Benchmark", AuthIDs: ids, Models: []string{"gpt"}, Enabled: true}}
	app.state.KeyBindings = map[string]KeyBinding{hash: {APIKeyHash: hash, PoolID: "benchmark"}}
	raw, err := json.Marshal(SchedulerPickRequest{
		Model:      "gpt",
		Provider:   "codex",
		Options:    SchedulerPickOptions{Headers: map[string][]string{"Authorization": {"Bearer " + apiKey}}},
		Candidates: candidates,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		if _, err := app.pickScheduler(raw); err != nil {
			b.Fatal(err)
		}
	}
}
