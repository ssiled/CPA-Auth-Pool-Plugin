package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type App struct {
	mu        sync.RWMutex
	state     State
	stateFile string
}

type State struct {
	Pools       []PoolConfig          `json:"pools"`
	KeyBindings map[string]KeyBinding `json:"key_bindings"`
	AuthModels  map[string][]string   `json:"auth_models,omitempty"`
}

type PoolConfig struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	AuthIDs      []string `json:"auth_ids"`
	AccountTypes []string `json:"account_types,omitempty"`
	Models       []string `json:"models,omitempty"`
	Enabled      bool     `json:"enabled"`
}

type KeyBinding struct {
	APIKeyHash string `json:"api_key_hash"`
	PoolID     string `json:"pool_id"`
	UserID     int    `json:"user_id,omitempty"`
	Username   string `json:"username,omitempty"`
}

func NewApp() *App {
	return &App{state: State{Pools: []PoolConfig{}, KeyBindings: map[string]KeyBinding{}, AuthModels: map[string][]string{}}}
}

func (a *App) Shutdown() {
	_ = a.save()
}

func (a *App) HandleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case MethodPluginRegister, MethodPluginReconfigure:
		if err := a.configure(request); err != nil {
			return nil, err
		}
		return OKEnvelope(a.registration())
	case MethodSchedulerPick:
		return a.pickScheduler(request)
	case MethodResponseIntercept:
		return a.interceptResponse(request)
	case MethodManagementRegister:
		return OKEnvelope(a.managementRegistration())
	case MethodManagementHandle:
		return a.handleManagement(request)
	default:
		return ErrorEnvelope("unknown_method", "unknown method: "+method, http.StatusNotFound), nil
	}
}

func (a *App) configure(raw []byte) error {
	stateFile := "cpa-auth-pool-state.json"
	if len(raw) > 0 {
		var req LifecycleRequest
		if err := json.Unmarshal(raw, &req); err == nil && strings.Contains(string(req.ConfigYAML), "state_file") {
			for _, line := range strings.Split(string(req.ConfigYAML), "\n") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 && strings.TrimSpace(parts[0]) == "state_file" {
					stateFile = strings.Trim(strings.TrimSpace(parts[1]), "'\"")
				}
			}
		}
	}
	a.mu.Lock()
	a.stateFile = stateFile
	a.mu.Unlock()
	return a.load()
}

func (a *App) registration() Registration {
	return Registration{
		SchemaVersion: SchemaVersion,
		Metadata: Metadata{
			Name:             PluginName,
			Version:          Version,
			Author:           "CPA-Helper-s",
			GitHubRepository: "https://github.com/ssiled/CPA-Auth-Pool-Plugin",
			ConfigFields: []ConfigField{
				{Name: "state_file", Type: "string", Description: "JSON state file used for auth pools and API key bindings."},
			},
		},
		Capabilities: Capabilities{Scheduler: true, ResponseInterceptor: true, ManagementAPI: true},
	}
}

func (a *App) pickScheduler(raw []byte) ([]byte, error) {
	var req SchedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	apiKey := extractAPIKey(req.Options.Headers)
	if apiKey == "" {
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	apiKeyHash := hashAPIKey(apiKey)
	a.mu.RLock()
	binding, ok := a.state.KeyBindings[apiKeyHash]
	pool, poolOK := a.poolLocked(binding.PoolID)
	allowedModels := a.poolModelsLocked(pool)
	a.mu.RUnlock()
	if !ok {
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	if !poolOK || !pool.Enabled {
		return OKEnvelope(SchedulerPickResponse{Handled: true})
	}
	if requestedModel := strings.TrimSpace(req.Model); requestedModel != "" {
		if _, ok := allowedModels[requestedModel]; !ok {
			return OKEnvelope(SchedulerPickResponse{Handled: true})
		}
	}
	allowed := make(map[string]struct{}, len(pool.AuthIDs))
	for _, id := range pool.AuthIDs {
		if id = strings.TrimSpace(id); id != "" {
			allowed[id] = struct{}{}
		}
	}
	allowedTypes := make(map[string]struct{}, len(pool.AccountTypes))
	for _, accountType := range pool.AccountTypes {
		if accountType = strings.ToLower(strings.TrimSpace(accountType)); accountType != "" {
			allowedTypes[accountType] = struct{}{}
		}
	}
	matched := make([]SchedulerAuthCandidate, 0, len(req.Candidates))
	for _, candidate := range req.Candidates {
		if _, ok := allowed[candidate.ID]; ok {
			matched = append(matched, candidate)
			continue
		}
		if _, ok := allowedTypes[candidateAccountType(candidate)]; ok {
			matched = append(matched, candidate)
		}
	}
	if len(matched) == 0 {
		return OKEnvelope(SchedulerPickResponse{Handled: true})
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].Priority == matched[j].Priority {
			return matched[i].ID < matched[j].ID
		}
		return matched[i].Priority > matched[j].Priority
	})
	return OKEnvelope(SchedulerPickResponse{Handled: true, AuthID: matched[0].ID})
}

func candidateAccountType(candidate SchedulerAuthCandidate) string {
	if candidate.Attributes == nil {
		return "supported"
	}
	for _, key := range []string{"account_type", "plan_type", "tier", "chatgpt_plan_type", "chatgptPlanType", "planType"} {
		if value := strings.ToLower(strings.TrimSpace(candidate.Attributes[key])); value != "" {
			return value
		}
	}
	return "supported"
}

func (a *App) poolLocked(id string) (PoolConfig, bool) {
	for _, pool := range a.state.Pools {
		if pool.ID == id {
			return pool, true
		}
	}
	return PoolConfig{}, false
}

func (a *App) load() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stateFile == "" {
		a.stateFile = "cpa-auth-pool-state.json"
	}
	raw, err := os.ReadFile(a.stateFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			a.state = State{Pools: []PoolConfig{}, KeyBindings: map[string]KeyBinding{}, AuthModels: map[string][]string{}}
			return nil
		}
		return err
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	if state.Pools == nil {
		state.Pools = []PoolConfig{}
	}
	if state.KeyBindings == nil {
		state.KeyBindings = map[string]KeyBinding{}
	}
	if state.AuthModels == nil {
		state.AuthModels = map[string][]string{}
	}
	a.state = state
	return nil
}

func (a *App) save() error {
	a.mu.RLock()
	state := a.state
	stateFile := a.stateFile
	a.mu.RUnlock()
	if stateFile == "" {
		stateFile = "cpa-auth-pool-state.json"
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil && filepath.Dir(stateFile) != "." {
		return err
	}
	return os.WriteFile(stateFile, raw, 0o600)
}

func hashAPIKey(apiKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(apiKey)))
	return hex.EncodeToString(sum[:])
}

func extractAPIKey(headers map[string][]string) string {
	for name, values := range headers {
		if len(values) == 0 {
			continue
		}
		if strings.EqualFold(name, "Authorization") {
			parts := strings.Fields(values[0])
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				return strings.TrimSpace(parts[1])
			}
		}
		if strings.EqualFold(name, "X-API-Key") || strings.EqualFold(name, "api-key") || strings.EqualFold(name, "x-api-key") {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}
