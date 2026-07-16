package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (a *App) managementRegistration() ManagementRegistrationResponse {
	base := "/v0/management/plugins/" + PluginID
	return ManagementRegistrationResponse{Routes: []ManagementRoute{
		{Method: http.MethodGet, Path: base + "/status", Description: "List auth pools and API key bindings."},
		{Method: http.MethodGet, Path: base + "/pools", Description: "List auth pools."},
		{Method: http.MethodPost, Path: base + "/pools", Description: "Create or update an auth pool."},
		{Method: http.MethodDelete, Path: base + "/pools", Description: "Delete an auth pool."},
		{Method: http.MethodPost, Path: base + "/auth-models", Description: "Sync per-auth model catalogs used to filter /v1/models."},
		{Method: http.MethodGet, Path: base + "/bindings", Description: "List API key to pool bindings."},
		{Method: http.MethodPost, Path: base + "/bindings", Description: "Bind an API key hash to an auth pool."},
		{Method: http.MethodDelete, Path: base + "/bindings", Description: "Remove an API key binding."},
	}}
}

func (a *App) handleManagement(raw []byte) ([]byte, error) {
	var req ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	base := "/v0/management/plugins/" + PluginID
	path := strings.TrimRight(req.Path, "/")
	switch {
	case req.Method == http.MethodGet && path == base+"/status":
		return OKEnvelope(jsonResponse(http.StatusOK, a.snapshot()))
	case req.Method == http.MethodGet && path == base+"/pools":
		return OKEnvelope(jsonResponse(http.StatusOK, map[string]any{"pools": a.snapshot().Pools}))
	case req.Method == http.MethodPost && path == base+"/pools":
		return OKEnvelope(a.upsertPool(req.Body))
	case req.Method == http.MethodDelete && path == base+"/pools":
		return OKEnvelope(a.deletePool(idFromRequest(req)))
	case req.Method == http.MethodPost && path == base+"/auth-models":
		return OKEnvelope(a.syncAuthModels(req.Body))
	case req.Method == http.MethodGet && path == base+"/bindings":
		return OKEnvelope(jsonResponse(http.StatusOK, map[string]any{"bindings": a.snapshot().Bindings}))
	case req.Method == http.MethodPost && path == base+"/bindings":
		return OKEnvelope(a.upsertBinding(req.Body))
	case req.Method == http.MethodDelete && path == base+"/bindings":
		return OKEnvelope(a.deleteBinding(hashFromRequest(req)))
	default:
		return OKEnvelope(jsonError(http.StatusNotFound, "not_found", "route not found"))
	}
}

type statusSnapshot struct {
	Pools      []PoolConfig        `json:"pools"`
	Bindings   []KeyBinding        `json:"bindings"`
	AuthModels map[string][]string `json:"auth_models,omitempty"`
}

func (a *App) snapshot() statusSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	bindings := make([]KeyBinding, 0, len(a.state.KeyBindings))
	for _, binding := range a.state.KeyBindings {
		bindings = append(bindings, binding)
	}
	authModels := make(map[string][]string, len(a.state.AuthModels))
	for authID, models := range a.state.AuthModels {
		authModels[authID] = append([]string(nil), models...)
	}
	return statusSnapshot{Pools: append([]PoolConfig(nil), a.state.Pools...), Bindings: bindings, AuthModels: authModels}
}

func (a *App) upsertPool(body []byte) ManagementResponse {
	var pool PoolConfig
	if err := json.Unmarshal(body, &pool); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	pool.ID = strings.TrimSpace(pool.ID)
	pool.Name = strings.TrimSpace(pool.Name)
	pool.AuthIDs = cleanStringList(pool.AuthIDs)
	pool.AccountTypes = cleanLowerStringList(pool.AccountTypes)
	pool.Models = cleanModelList(pool.Models)
	if pool.ID == "" || pool.Name == "" {
		return jsonError(http.StatusBadRequest, "invalid_pool", "id and name are required")
	}
	pool.Enabled = true
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(body, &raw)
	_, modelsProvided := raw["models"]
	a.mu.Lock()
	found := false
	for i := range a.state.Pools {
		if a.state.Pools[i].ID == pool.ID {
			if !modelsProvided {
				pool.Models = append([]string(nil), a.state.Pools[i].Models...)
			}
			a.state.Pools[i] = pool
			found = true
			break
		}
	}
	if !found {
		a.state.Pools = append(a.state.Pools, pool)
	}
	a.mu.Unlock()
	if err := a.save(); err != nil {
		return jsonError(http.StatusInternalServerError, "save_failed", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"pool": pool})
}

func (a *App) deletePool(id string) ManagementResponse {
	id = strings.TrimSpace(id)
	if id == "" {
		return jsonError(http.StatusBadRequest, "missing_id", "id is required")
	}
	a.mu.Lock()
	next := a.state.Pools[:0]
	for _, pool := range a.state.Pools {
		if pool.ID != id {
			next = append(next, pool)
		}
	}
	a.state.Pools = next
	for hash, binding := range a.state.KeyBindings {
		if binding.PoolID == id {
			delete(a.state.KeyBindings, hash)
		}
	}
	a.mu.Unlock()
	if err := a.save(); err != nil {
		return jsonError(http.StatusInternalServerError, "save_failed", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"deleted": true, "id": id})
}

type authModelsPayload struct {
	AuthModels map[string][]string `json:"auth_models"`
}

func (a *App) syncAuthModels(body []byte) ManagementResponse {
	var payload authModelsPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	next := make(map[string][]string, len(payload.AuthModels))
	for authID, models := range payload.AuthModels {
		authID = strings.TrimSpace(authID)
		if authID == "" {
			continue
		}
		next[authID] = cleanModelList(models)
	}
	a.mu.Lock()
	a.state.AuthModels = next
	for i := range a.state.Pools {
		a.state.Pools[i].Models = cleanModelList(poolModelListFromAuthModels(a.state.Pools[i].AuthIDs, next))
	}
	a.mu.Unlock()
	if err := a.save(); err != nil {
		return jsonError(http.StatusInternalServerError, "save_failed", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"synced": true, "auth_count": len(next)})
}

func poolModelListFromAuthModels(authIDs []string, authModels map[string][]string) []string {
	models := []string{}
	for _, authID := range authIDs {
		models = append(models, authModels[strings.TrimSpace(authID)]...)
	}
	return models
}

func (a *App) upsertBinding(body []byte) ManagementResponse {
	var binding KeyBinding
	if err := json.Unmarshal(body, &binding); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	binding.APIKeyHash = strings.TrimSpace(binding.APIKeyHash)
	binding.PoolID = strings.TrimSpace(binding.PoolID)
	if binding.APIKeyHash == "" || binding.PoolID == "" {
		return jsonError(http.StatusBadRequest, "invalid_binding", "api_key_hash and pool_id are required")
	}
	a.mu.Lock()
	if a.state.KeyBindings == nil {
		a.state.KeyBindings = map[string]KeyBinding{}
	}
	a.state.KeyBindings[binding.APIKeyHash] = binding
	a.mu.Unlock()
	if err := a.save(); err != nil {
		return jsonError(http.StatusInternalServerError, "save_failed", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"binding": binding})
}

func (a *App) deleteBinding(hash string) ManagementResponse {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return jsonError(http.StatusBadRequest, "missing_api_key_hash", "api_key_hash is required")
	}
	a.mu.Lock()
	delete(a.state.KeyBindings, hash)
	a.mu.Unlock()
	if err := a.save(); err != nil {
		return jsonError(http.StatusInternalServerError, "save_failed", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"deleted": true, "api_key_hash": hash})
}

func idFromRequest(req ManagementRequest) string {
	if value := req.Query.Get("id"); value != "" {
		return value
	}
	var body map[string]string
	_ = json.Unmarshal(req.Body, &body)
	return body["id"]
}

func hashFromRequest(req ManagementRequest) string {
	if value := req.Query.Get("api_key_hash"); value != "" {
		return value
	}
	var body map[string]string
	_ = json.Unmarshal(req.Body, &body)
	return body["api_key_hash"]
}

func cleanStringList(values []string) []string {
	seen := map[string]struct{}{}
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		cleaned = append(cleaned, value)
	}
	return cleaned
}

func cleanLowerStringList(values []string) []string {
	seen := map[string]struct{}{}
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		cleaned = append(cleaned, value)
	}
	return cleaned
}

func jsonResponse(status int, body any) ManagementResponse {
	raw, _ := json.Marshal(body)
	return ManagementResponse{StatusCode: status, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: raw}
}

func jsonError(status int, code, message string) ManagementResponse {
	return jsonResponse(status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
