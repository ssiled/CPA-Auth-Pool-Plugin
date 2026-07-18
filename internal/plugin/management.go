package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (a *App) managementRegistration() ManagementRegistrationResponse {
	base := "/v0/management/plugins/" + PluginID
	return ManagementRegistrationResponse{Routes: []ManagementRoute{
		{Method: http.MethodGet, Path: base + "/status", Description: "List auth pools, API key bindings, and Codex concurrency limits."},
		{Method: http.MethodGet, Path: base + "/pools", Description: "List auth pools."},
		{Method: http.MethodPost, Path: base + "/pools", Description: "Create or update an auth pool."},
		{Method: http.MethodDelete, Path: base + "/pools", Description: "Delete an auth pool."},
		{Method: http.MethodPost, Path: base + "/auth-models", Description: "Sync per-auth model catalogs used to filter /v1/models."},
		{Method: http.MethodPost, Path: base + "/auth-priorities", Description: "Sync account types and scheduler-only priorities."},
		{Method: http.MethodPost, Path: base + "/proxy-keys", Description: "Register trusted CPA API keys used by CPA-Helper forwarding."},
		{Method: http.MethodGet, Path: base + "/bindings", Description: "List API key to pool bindings."},
		{Method: http.MethodPost, Path: base + "/bindings", Description: "Bind an API key hash to an auth pool."},
		{Method: http.MethodDelete, Path: base + "/bindings", Description: "Remove an API key binding."},
		{Method: http.MethodPost, Path: base + "/codex-concurrency-limits", Description: "Configure per-tier Codex concurrency limits."},
		{Method: http.MethodDelete, Path: base + "/concurrency-slots", Description: "Reset one or all in-flight concurrency slots."},
		{Method: http.MethodGet, Path: base + "/events", Description: "List recent scheduler and upstream completion events."},
		{Method: http.MethodDelete, Path: base + "/events", Description: "Clear recent scheduler and upstream completion events."},
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
	case req.Method == http.MethodPost && path == base+"/auth-priorities":
		return OKEnvelope(a.updateAuthPriorities(req.Body))
	case req.Method == http.MethodPost && path == base+"/proxy-keys":
		return OKEnvelope(a.upsertProxyKeys(req.Body))
	case req.Method == http.MethodGet && path == base+"/bindings":
		return OKEnvelope(jsonResponse(http.StatusOK, map[string]any{"bindings": a.snapshot().Bindings}))
	case req.Method == http.MethodPost && path == base+"/bindings":
		return OKEnvelope(a.upsertBinding(req.Body))
	case req.Method == http.MethodDelete && path == base+"/bindings":
		return OKEnvelope(a.deleteBinding(hashFromRequest(req)))
	case req.Method == http.MethodPost && path == base+"/codex-concurrency-limits":
		return OKEnvelope(a.updateCodexConcurrencyLimits(req.Body))
	case req.Method == http.MethodDelete && path == base+"/concurrency-slots":
		return OKEnvelope(a.deleteConcurrencySlot(req))
	case req.Method == http.MethodGet && path == base+"/events":
		return OKEnvelope(jsonResponse(http.StatusOK, a.pluginEventSnapshot(eventLimitFromRequest(req))))
	case req.Method == http.MethodDelete && path == base+"/events":
		return OKEnvelope(jsonResponse(http.StatusOK, map[string]any{"cleared": a.clearPluginEvents()}))
	default:
		return OKEnvelope(jsonError(http.StatusNotFound, "not_found", "route not found"))
	}
}

type statusSnapshot struct {
	PluginVersion          string                    `json:"plugin_version"`
	ConcurrencyScope       string                    `json:"concurrency_scope"`
	ConcurrencyStrategy    string                    `json:"concurrency_strategy"`
	SchedulerPriorities    bool                      `json:"scheduler_priorities"`
	Pools                  []PoolConfig              `json:"pools"`
	Bindings               []KeyBinding              `json:"bindings"`
	AuthModels             map[string][]string       `json:"auth_models,omitempty"`
	AuthTypes              map[string]string         `json:"auth_types,omitempty"`
	TypePriorities         map[string]int            `json:"type_priorities,omitempty"`
	AuthPriorityOverrides  map[string]int            `json:"auth_priority_overrides,omitempty"`
	ProxyKeyCount          int                       `json:"proxy_key_count"`
	CodexConcurrencyLimits map[string]int            `json:"codex_concurrency_limits"`
	Concurrency            concurrencySnapshot       `json:"concurrency"`
	ConcurrencySlots       []concurrencySlotSnapshot `json:"concurrency_slots,omitempty"`
}

type concurrencySnapshot struct {
	Counts map[string]int `json:"counts"`
	Limits map[string]int `json:"limits"`
}

type concurrencySlotSnapshot struct {
	AuthID           string `json:"auth_id"`
	Tier             string `json:"tier"`
	Count            int    `json:"count"`
	StartedAt        string `json:"started_at,omitempty"`
	StartedAtUnix    int64  `json:"started_at_unix,omitempty"`
	ExpiresAt        string `json:"expires_at"`
	ExpiresAtUnix    int64  `json:"expires_at_unix"`
	RemainingSeconds int64  `json:"remaining_seconds"`
}

func (a *App) snapshot() statusSnapshot {
	now := time.Now()
	a.clearExpiredConcurrencySlots(now)
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
	limits := cloneConcurrencyLimits(a.state.CodexConcurrencyLimits)
	counts := a.codexConcurrencyCountsLocked(now)
	return statusSnapshot{
		PluginVersion:          Version,
		ConcurrencyScope:       "per_account",
		ConcurrencyStrategy:    "least_loaded_round_robin",
		SchedulerPriorities:    true,
		Pools:                  append([]PoolConfig(nil), a.state.Pools...),
		Bindings:               bindings,
		AuthModels:             authModels,
		AuthTypes:              cloneStringMap(a.state.AuthTypes),
		TypePriorities:         cloneIntMap(a.state.TypePriorities),
		AuthPriorityOverrides:  cloneIntMap(a.state.AuthPriorityOverrides),
		ProxyKeyCount:          len(a.state.ProxyKeyHashes),
		CodexConcurrencyLimits: limits,
		Concurrency:            concurrencySnapshot{Counts: counts, Limits: limits},
		ConcurrencySlots:       concurrencySlotSnapshots(now, a.state.ConcurrencySlots),
	}
}

type authPrioritiesPayload struct {
	AuthTypes             map[string]string `json:"auth_types"`
	TypePriorities        map[string]int    `json:"type_priorities"`
	AuthPriorityOverrides map[string]int    `json:"auth_priority_overrides"`
	RemoveOverrides       []string          `json:"remove_overrides"`
	ReplaceOverrides      bool              `json:"replace_overrides"`
}

func (a *App) updateAuthPriorities(body []byte) ManagementResponse {
	var payload authPrioritiesPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(body, &raw)
	if err := validateLogicalPriorities("type_priorities", payload.TypePriorities); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_priority", err.Error())
	}
	if err := validateLogicalPriorities("auth_priority_overrides", payload.AuthPriorityOverrides); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_priority", err.Error())
	}
	nextTypes := normalizeAuthTypes(payload.AuthTypes)
	nextTypePriorities := normalizeTypePriorities(payload.TypePriorities)
	nextOverrides := normalizeAuthPriorityOverrides(payload.AuthPriorityOverrides)

	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	a.mu.RLock()
	nextState := cloneState(a.state)
	stateFile := a.stateFile
	a.mu.RUnlock()
	if _, provided := raw["auth_types"]; provided {
		nextState.AuthTypes = nextTypes
	}
	if _, provided := raw["type_priorities"]; provided {
		nextState.TypePriorities = nextTypePriorities
	}
	if payload.ReplaceOverrides {
		nextState.AuthPriorityOverrides = nextOverrides
	} else {
		if nextState.AuthPriorityOverrides == nil {
			nextState.AuthPriorityOverrides = map[string]int{}
		}
		for authID, priority := range nextOverrides {
			nextState.AuthPriorityOverrides[authID] = priority
		}
	}
	for _, authID := range payload.RemoveOverrides {
		delete(nextState.AuthPriorityOverrides, normalizeAuthIDKey(authID))
	}
	if err := persistState(nextState, stateFile); err != nil {
		return jsonError(http.StatusInternalServerError, "save_failed", err.Error())
	}
	a.mu.Lock()
	a.state.AuthTypes = nextState.AuthTypes
	a.state.TypePriorities = nextState.TypePriorities
	a.state.AuthPriorityOverrides = nextState.AuthPriorityOverrides
	a.mu.Unlock()
	return jsonResponse(http.StatusOK, map[string]any{"status": a.snapshot()})
}

func validateLogicalPriorities(field string, values map[string]int) error {
	for key, priority := range values {
		if priority < minLogicalPriority || priority > maxLogicalPriority {
			return fmt.Errorf("%s[%q] must be between %d and %d", field, strings.TrimSpace(key), minLogicalPriority, maxLogicalPriority)
		}
	}
	return nil
}

func normalizeAuthTypes(values map[string]string) map[string]string {
	normalized := make(map[string]string, len(values))
	for authID, accountType := range values {
		authID = normalizeAuthIDKey(authID)
		accountType = normalizeAccountType(accountType)
		if authID != "" && accountType != "" {
			normalized[authID] = accountType
		}
	}
	return normalized
}

func normalizeTypePriorities(values map[string]int) map[string]int {
	normalized := make(map[string]int, len(values))
	for accountType, priority := range values {
		accountType = normalizeAccountType(accountType)
		if accountType != "" {
			normalized[accountType] = priority
		}
	}
	return normalized
}

func normalizeAuthPriorityOverrides(values map[string]int) map[string]int {
	normalized := make(map[string]int, len(values))
	for authID, priority := range values {
		authID = normalizeAuthIDKey(authID)
		if authID != "" {
			normalized[authID] = priority
		}
	}
	return normalized
}

func cloneConcurrencyLimits(limits map[string]int) map[string]int {
	if len(limits) == 0 {
		limits = defaultCodexConcurrencyLimits()
	}
	cloned := make(map[string]int, len(limits))
	for tier, limit := range limits {
		tier = normalizeConcurrencyTier(tier)
		if tier == "" || limit < 0 {
			continue
		}
		cloned[tier] = limit
	}
	return cloned
}

func concurrencySlotSnapshots(now time.Time, slots map[string]ConcurrencySlot) []concurrencySlotSnapshot {
	items := make([]concurrencySlotSnapshot, 0, len(slots))
	for authID, slot := range slots {
		if slot.ExpiresAt.IsZero() || !now.Before(slot.ExpiresAt) {
			continue
		}
		count := slot.Count
		if count <= 0 {
			count = 1
		}
		item := concurrencySlotSnapshot{
			AuthID:           authID,
			Tier:             normalizeConcurrencyTier(slot.Tier),
			Count:            count,
			ExpiresAt:        slot.ExpiresAt.Format(time.RFC3339),
			ExpiresAtUnix:    slot.ExpiresAt.Unix(),
			RemainingSeconds: int64(slot.ExpiresAt.Sub(now).Seconds()),
		}
		if !slot.StartedAt.IsZero() {
			item.StartedAt = slot.StartedAt.Format(time.RFC3339)
			item.StartedAtUnix = slot.StartedAt.Unix()
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Tier == items[j].Tier {
			return items[i].AuthID < items[j].AuthID
		}
		return items[i].Tier < items[j].Tier
	})
	return items
}

func (a *App) updateCodexConcurrencyLimits(body []byte) ManagementResponse {
	var payload struct {
		Limits map[string]int `json:"limits"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	next := normalizeConcurrencyLimits(payload.Limits)
	a.mu.Lock()
	a.state.CodexConcurrencyLimits = next
	a.mu.Unlock()
	if err := a.save(); err != nil {
		return jsonError(http.StatusInternalServerError, "save_failed", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"codex_concurrency_limits": next, "status": a.snapshot()})
}

func normalizeConcurrencyLimits(limits map[string]int) map[string]int {
	defaults := defaultCodexConcurrencyLimits()
	if limits == nil {
		return defaults
	}
	next := map[string]int{}
	for tier, limit := range limits {
		tier = normalizeConcurrencyTier(tier)
		if tier == "" || limit < 0 {
			continue
		}
		next[tier] = limit
	}
	for tier, limit := range defaults {
		if _, ok := next[tier]; !ok {
			next[tier] = limit
		}
	}
	return next
}

func (a *App) deleteConcurrencySlot(req ManagementRequest) ManagementResponse {
	all := strings.EqualFold(req.Query.Get("all"), "true")
	authID := strings.TrimSpace(req.Query.Get("auth_id"))
	if len(req.Body) > 0 {
		var body struct {
			All    bool   `json:"all"`
			AuthID string `json:"auth_id"`
		}
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
		}
		all = all || body.All
		if strings.TrimSpace(body.AuthID) != "" {
			authID = strings.TrimSpace(body.AuthID)
		}
	}
	a.mu.Lock()
	removed := 0
	if all {
		removed = len(a.state.ConcurrencySlots)
		a.state.ConcurrencySlots = map[string]ConcurrencySlot{}
	} else {
		if authID == "" {
			a.mu.Unlock()
			return jsonError(http.StatusBadRequest, "missing_auth_id", "auth_id or all=true is required")
		}
		if _, ok := a.state.ConcurrencySlots[authID]; ok {
			delete(a.state.ConcurrencySlots, authID)
			removed = 1
		}
	}
	a.mu.Unlock()
	if err := a.save(); err != nil {
		return jsonError(http.StatusInternalServerError, "save_failed", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"removed": removed, "auth_id": authID, "status": a.snapshot()})
}

func (a *App) upsertPool(body []byte) ManagementResponse {
	var pool PoolConfig
	if err := json.Unmarshal(body, &pool); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	pool.ID = strings.TrimSpace(pool.ID)
	pool.Name = strings.TrimSpace(pool.Name)
	pool.AuthIDs = cleanStringList(pool.AuthIDs)
	pool.ResolvedAuthIDs = cleanStringList(pool.ResolvedAuthIDs)
	pool.AccountTypes = cleanLowerStringList(pool.AccountTypes)
	pool.Providers = cleanLowerStringList(pool.Providers)
	pool.Models = cleanModelList(pool.Models)
	if pool.ID == "" || pool.Name == "" {
		return jsonError(http.StatusBadRequest, "invalid_pool", "id and name are required")
	}
	pool.Enabled = true
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(body, &raw)
	_, modelsProvided := raw["models"]
	_, resolvedAuthIDsProvided := raw["resolved_auth_ids"]
	a.mu.Lock()
	found := false
	for i := range a.state.Pools {
		if a.state.Pools[i].ID == pool.ID {
			if !modelsProvided {
				pool.Models = append([]string(nil), a.state.Pools[i].Models...)
			}
			if !resolvedAuthIDsProvided {
				pool.ResolvedAuthIDs = append([]string(nil), a.state.Pools[i].ResolvedAuthIDs...)
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
	AuthModels          map[string][]string `json:"auth_models"`
	PoolModels          map[string][]string `json:"pool_models"`
	PoolResolvedAuthIDs map[string][]string `json:"pool_resolved_auth_ids"`
}

func (a *App) syncAuthModels(body []byte) ManagementResponse {
	var payload authModelsPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(body, &raw)
	_, authModelsProvided := raw["auth_models"]
	_, poolModelsProvided := raw["pool_models"]
	next := make(map[string][]string, len(payload.AuthModels))
	for authID, models := range payload.AuthModels {
		authID = strings.TrimSpace(authID)
		if authID == "" {
			continue
		}
		next[authID] = cleanModelList(models)
	}
	nextPoolModels := make(map[string][]string, len(payload.PoolModels))
	for poolID, models := range payload.PoolModels {
		poolID = strings.TrimSpace(poolID)
		if poolID == "" {
			continue
		}
		nextPoolModels[poolID] = cleanModelList(models)
	}
	nextPoolResolvedAuthIDs := make(map[string][]string, len(payload.PoolResolvedAuthIDs))
	for poolID, authIDs := range payload.PoolResolvedAuthIDs {
		poolID = strings.TrimSpace(poolID)
		if poolID == "" {
			continue
		}
		nextPoolResolvedAuthIDs[poolID] = cleanStringList(authIDs)
	}
	a.mu.Lock()
	if authModelsProvided {
		a.state.AuthModels = next
	}
	for i := range a.state.Pools {
		if authIDs, ok := nextPoolResolvedAuthIDs[a.state.Pools[i].ID]; ok {
			a.state.Pools[i].ResolvedAuthIDs = authIDs
		}
		if poolModelsProvided {
			if models, ok := nextPoolModels[a.state.Pools[i].ID]; ok {
				a.state.Pools[i].Models = models
				continue
			}
			a.state.Pools[i].Models = cleanModelList(poolModelListFromAuthModels(poolCandidateAuthIDs(a.state.Pools[i]), next))
		}
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

type proxyKeysPayload struct {
	APIKeys []string `json:"api_keys"`
	APIKey  string   `json:"api_key"`
}

func (a *App) upsertProxyKeys(body []byte) ManagementResponse {
	var payload proxyKeysPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	keys := append([]string(nil), payload.APIKeys...)
	if strings.TrimSpace(payload.APIKey) != "" {
		keys = append(keys, payload.APIKey)
	}
	hashes := cleanAPIKeyHashes(keys)
	if len(hashes) == 0 {
		return jsonError(http.StatusBadRequest, "missing_api_key", "api_key is required")
	}
	a.mu.Lock()
	a.state.ProxyKeyHashes = hashes
	a.mu.Unlock()
	if err := a.save(); err != nil {
		return jsonError(http.StatusInternalServerError, "save_failed", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"proxy_key_count": len(hashes)})
}

func cleanAPIKeyHashes(keys []string) []string {
	seen := map[string]struct{}{}
	result := []string{}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		hash := hashAPIKey(key)
		if _, ok := seen[hash]; ok {
			continue
		}
		seen[hash] = struct{}{}
		result = append(result, hash)
	}
	return result
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
