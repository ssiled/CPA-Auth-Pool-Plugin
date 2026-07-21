package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type App struct {
	mu                sync.RWMutex
	state             State
	stateFile         string
	saveMu            sync.Mutex
	schedulerMu       sync.Mutex
	schedulerCursors  map[string]int
	eventsMu          sync.RWMutex
	events            []PluginEvent
	eventStart        int
	nextEventID       uint64
	pendingMu         sync.Mutex
	pendingSelections []pendingPluginSelection
	nextAttributionID uint64
}

const helperAPIKeyHashHeader = "X-CPA-Helper-API-Key-Hash"

const legacyStateFile = "cpa-auth-pool-state.json"

const (
	minLogicalPriority = 0
	maxLogicalPriority = 100

	poolSchedulingRoundRobin = "round-robin"
	poolSchedulingFillFirst  = "fill-first"
)

type State struct {
	Pools                  []PoolConfig                   `json:"pools"`
	KeyBindings            map[string]KeyBinding          `json:"key_bindings"`
	AuthModels             map[string][]string            `json:"auth_models,omitempty"`
	AuthTypes              map[string]string              `json:"auth_types,omitempty"`
	TypePriorities         map[string]int                 `json:"type_priorities,omitempty"`
	AuthPriorityOverrides  map[string]int                 `json:"auth_priority_overrides,omitempty"`
	ProxyKeyHashes         []string                       `json:"proxy_key_hashes,omitempty"`
	CodexConcurrencyLimits map[string]int                 `json:"codex_concurrency_limits,omitempty"`
	ConcurrencySlots       map[string]ConcurrencySlot     `json:"concurrency_slots,omitempty"`
	PoolConcurrencySlots   map[string]PoolConcurrencySlot `json:"pool_concurrency_slots,omitempty"`
	FailureCooldowns       map[string]FailureCooldown     `json:"failure_cooldowns,omitempty"`
	ModelCooldowns         map[string]ModelCooldown       `json:"model_cooldowns,omitempty"`
}

type PoolConfig struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Description        string   `json:"description,omitempty"`
	AuthIDs            []string `json:"auth_ids"`
	ResolvedAuthIDs    []string `json:"resolved_auth_ids,omitempty"`
	AccountTypes       []string `json:"account_types,omitempty"`
	Providers          []string `json:"providers,omitempty"`
	Models             []string `json:"models,omitempty"`
	SchedulingStrategy string   `json:"scheduling_strategy"`
	MaxConcurrency     int      `json:"max_concurrency"`
	Enabled            bool     `json:"enabled"`
}

type KeyBinding struct {
	APIKeyHash        string `json:"api_key_hash"`
	APIKeyDescription string `json:"api_key_description,omitempty"`
	PoolID            string `json:"pool_id"`
	UserID            int    `json:"user_id,omitempty"`
	Username          string `json:"username,omitempty"`
}

func NewApp() *App {
	return &App{
		state: State{
			Pools: []PoolConfig{}, KeyBindings: map[string]KeyBinding{}, AuthModels: map[string][]string{},
			AuthTypes: map[string]string{}, TypePriorities: map[string]int{}, AuthPriorityOverrides: map[string]int{},
			ProxyKeyHashes: []string{}, CodexConcurrencyLimits: defaultCodexConcurrencyLimits(), ConcurrencySlots: map[string]ConcurrencySlot{}, PoolConcurrencySlots: map[string]PoolConcurrencySlot{}, FailureCooldowns: map[string]FailureCooldown{}, ModelCooldowns: map[string]ModelCooldown{},
		},
		schedulerCursors:  map[string]int{},
		events:            make([]PluginEvent, 0, pluginEventCapacity),
		pendingSelections: make([]pendingPluginSelection, 0, pluginPendingSelectionCapacity),
	}
}

func (a *App) Shutdown() {
	_ = a.save()
}

func (a *App) HandleMethod(method string, request []byte) ([]byte, error) {
	return safePluginCall(func() ([]byte, error) {
		return a.handleMethod(method, request)
	})
}

func (a *App) handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case MethodPluginRegister, MethodPluginReconfigure:
		if err := a.configure(request); err != nil {
			return nil, err
		}
		return OKEnvelope(a.registration())
	case MethodModelRoute:
		return a.routeModel(request)
	case MethodSchedulerPick:
		return a.pickScheduler(request)
	case MethodUsageHandle:
		return a.handleUsage(request)
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

func safePluginCall(call func() ([]byte, error)) (response []byte, err error) {
	defer func() {
		if recover() != nil {
			response = nil
			err = errors.New("plugin panic recovered")
		}
	}()
	return call()
}

func (a *App) configure(raw []byte) error {
	stateFile := defaultStateFile()
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
	if err := validateStateFilePath(stateFile); err != nil {
		return err
	}
	a.mu.Lock()
	a.stateFile = resolveStateFile(stateFile)
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
		Capabilities: Capabilities{ModelRouter: true, Scheduler: true, ResponseInterceptor: true, ManagementAPI: true, UsagePlugin: true},
	}
}

func (a *App) pickScheduler(raw []byte) ([]byte, error) {
	var req SchedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	now := time.Now()
	failureCooldownsCleared := a.clearExpiredFailureCooldowns(now)
	modelCooldownsCleared := a.clearExpiredModelCooldowns(now)
	if failureCooldownsCleared || modelCooldownsCleared {
		a.saveRuntimeState()
	}
	apiKey := extractAPIKey(req.Options.Headers)
	apiKeyHash := extractHelperAPIKeyHash(req.Options.Headers)
	trustedProxyRequest := apiKeyHash != ""
	if apiKeyHash != "" && !a.isTrustedProxyAPIKey(apiKey) {
		a.recordSchedulerEvent(req, nil, nil, nil, nil, "blocked", "untrusted_proxy_key", http.StatusForbidden, now)
		return schedulerBlocked("untrusted_proxy_key", "helper API key hash header requires a trusted CPA proxy key", http.StatusForbidden)
	}
	if apiKeyHash == "" && apiKey != "" {
		apiKeyHash = hashAPIKey(apiKey)
	}
	if apiKeyHash == "" {
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	a.mu.RLock()
	binding, ok := a.state.KeyBindings[apiKeyHash]
	pool, poolOK := a.poolLocked(binding.PoolID)
	allowedModels := a.poolModelsLocked(pool)
	authTypes := cloneStringMap(a.state.AuthTypes)
	typePriorities := cloneIntMap(a.state.TypePriorities)
	authPriorityOverrides := cloneIntMap(a.state.AuthPriorityOverrides)
	failureCooldowns := cloneFailureCooldownMap(a.state.FailureCooldowns)
	modelCooldowns := cloneModelCooldownMap(a.state.ModelCooldowns)
	a.mu.RUnlock()
	if !ok {
		if trustedProxyRequest {
			a.recordSchedulerEvent(req, nil, nil, nil, nil, "blocked", "unbound_api_key", http.StatusForbidden, now)
			return schedulerBlocked("auth_pool_required", "trusted proxy request has no auth pool binding", http.StatusForbidden)
		}
		a.recordSchedulerEvent(req, nil, nil, nil, nil, "ignored", "unbound_api_key", 0, now)
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	if !poolOK || !pool.Enabled {
		a.recordSchedulerEvent(req, &binding, &pool, nil, nil, "blocked", "auth_pool_unavailable", http.StatusServiceUnavailable, now)
		return schedulerBlocked("auth_pool_unavailable", "bound auth pool is unavailable", http.StatusServiceUnavailable)
	}
	if requestedModel := strings.TrimSpace(req.Model); requestedModel != "" {
		if _, ok := allowedModels[normalizeModelID(requestedModel)]; !ok {
			a.recordSchedulerEvent(req, &binding, &pool, nil, nil, "blocked", "model_not_allowed", http.StatusForbidden, now)
			return schedulerBlocked("model_not_allowed", "requested model is outside the bound auth pool", http.StatusForbidden)
		}
	}
	allowed := make(map[string]struct{}, len(pool.AuthIDs))
	for _, id := range poolCandidateAuthIDs(pool) {
		if id = normalizeAuthIDKey(id); id != "" {
			allowed[id] = struct{}{}
		}
	}
	allowedTypes := make(map[string]struct{}, len(pool.AccountTypes))
	for _, accountType := range pool.AccountTypes {
		for _, normalized := range poolAccountTypeMatches(accountType) {
			allowedTypes[normalized] = struct{}{}
		}
	}
	allowedProviders := make(map[string]struct{}, len(pool.Providers))
	for _, provider := range pool.Providers {
		if provider = normalizeProviderKey(provider); provider != "" {
			allowedProviders[provider] = struct{}{}
		}
	}
	poolMatched := make([]SchedulerAuthCandidate, 0, len(req.Candidates))
	eligible := make([]SchedulerAuthCandidate, 0, len(req.Candidates))
	ruleExcluded := 0
	cooldownExcluded := 0
	modelCooldownExcluded := 0
	candidateTiers := map[string]string{}
	fallbackTier := poolFallbackConcurrencyTier(pool)
	reserveCandidate := func(candidate SchedulerAuthCandidate) bool {
		if tier, isCodex := candidateCodexConcurrencyTier(candidate); isCodex {
			normalizedTier := normalizeConcurrencyTier(tier)
			candidateTiers[candidate.ID] = normalizedTier
		} else if fallbackTier != "" {
			candidateTiers[candidate.ID] = fallbackTier
		}
		return true
	}
	applySchedulerPriority := func(candidate SchedulerAuthCandidate) SchedulerAuthCandidate {
		candidate.Priority = schedulerPriorityForCandidate(candidate, authTypes, typePriorities, authPriorityOverrides)
		return candidate
	}
	for _, candidate := range req.Candidates {
		matchedPool := false
		if _, ok := allowedProviders[normalizeProviderKey(candidate.Provider)]; ok {
			matchedPool = true
		} else if _, ok := allowed[normalizeAuthIDKey(candidate.ID)]; ok {
			if explicitCandidateConflictsWithPool(candidate, pool) {
				ruleExcluded++
				continue
			}
			matchedPool = true
		} else {
			candidateTypes := schedulerCandidateAccountTypes(candidate, authTypes)
			_, knownAccountType := authIDStringValue(authTypes, normalizeAuthIDKey(candidate.ID))
			if !knownAccountType && candidateConflictsWithPool(candidate, pool) {
				for _, candidateType := range candidateTypes {
					if _, ok := allowedTypes[candidateType]; ok {
						ruleExcluded++
						break
					}
				}
				continue
			}
			for _, candidateType := range candidateTypes {
				if _, ok := allowedTypes[candidateType]; ok {
					matchedPool = true
					break
				}
			}
		}
		if !matchedPool {
			continue
		}
		poolMatched = append(poolMatched, candidate)
		if candidate.Priority < 0 || !schedulerCandidateStatusEligible(candidate.Status) {
			continue
		}
		if failureCooldownActive(failureCooldowns, candidate.ID, now) {
			cooldownExcluded++
			continue
		}
		if modelCooldownActive(modelCooldowns, candidate.ID, req.Model, now) {
			modelCooldownExcluded++
			continue
		}
		if !reserveCandidate(candidate) {
			continue
		}
		eligible = append(eligible, applySchedulerPriority(candidate))
	}
	if len(poolMatched) == 0 {
		reason := "pool_no_matching_candidates"
		message := "bound auth pool has no matching auth candidates"
		if len(req.Candidates) == 0 {
			reason = "no_input_candidates"
			message = "host supplied no auth candidates"
		} else if ruleExcluded > 0 {
			reason = "plugin_rules_excluded"
			message = "all matching auth candidates were excluded by pool rules"
		} else if len(allowed) == 0 && len(allowedProviders) == 0 && len(allowedTypes) > 0 && len(authTypes) == 0 {
			reason = "account_metadata_not_synced"
			message = "account type or pool membership metadata has not been synchronized"
		}
		a.recordSchedulerEventDetails(req, &binding, &pool, poolMatched, eligible, nil, "blocked", reason, http.StatusServiceUnavailable, now)
		return schedulerBlocked("auth_pool_unavailable", message, http.StatusServiceUnavailable)
	}
	if len(eligible) == 0 {
		reason := schedulerIneligibleReason(poolMatched)
		if cooldownExcluded == len(poolMatched) {
			reason = "pool_candidates_cooling_down"
		} else if modelCooldownExcluded == len(poolMatched) {
			reason = "model_unsupported_by_pool_candidates"
		}
		a.recordSchedulerEventDetails(req, &binding, &pool, poolMatched, eligible, nil, "blocked", reason, http.StatusServiceUnavailable, now)
		return schedulerBlocked("auth_pool_unavailable", "bound auth pool has no eligible auth candidates", http.StatusServiceUnavailable)
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].Priority == eligible[j].Priority {
			return eligible[i].ID < eligible[j].ID
		}
		return eligible[i].Priority > eligible[j].Priority
	})
	schedulingStrategy := normalizedPoolSchedulingStrategy(pool.SchedulingStrategy)
	blockedByConcurrency := false
	for groupStart := 0; groupStart < len(eligible); {
		groupEnd := groupStart + 1
		for groupEnd < len(eligible) && eligible[groupEnd].Priority == eligible[groupStart].Priority {
			groupEnd++
		}
		group := eligible[groupStart:groupEnd]
		offset := 0
		if schedulingStrategy == poolSchedulingRoundRobin {
			offset = a.nextSchedulerCursor(schedulerCursorKey(pool.ID, req.Provider, req.Model, eligible[groupStart].Priority), len(group))
		}
		pick := a.selectAndReserveConcurrencyCandidate(group, candidateTiers, pool, offset, schedulingStrategy, now)
		blockedByConcurrency = blockedByConcurrency || pick.Blocked
		if pick.Selected {
			selected := pick.Candidate
			a.recordSchedulerEventDetails(req, &binding, &pool, poolMatched, eligible, &selected, "selected", "", http.StatusOK, now)
			return OKEnvelope(SchedulerPickResponse{Handled: true, AuthID: selected.ID})
		}
		groupStart = groupEnd
	}
	if blockedByConcurrency {
		a.recordSchedulerEventDetails(req, &binding, &pool, poolMatched, eligible, nil, "blocked", "auth_pool_busy", http.StatusTooManyRequests, now)
		return schedulerBlocked("auth_pool_busy", "bound auth pool accounts are at concurrency limit", http.StatusTooManyRequests)
	}
	a.recordSchedulerEventDetails(req, &binding, &pool, poolMatched, eligible, nil, "blocked", "no_available_candidates", http.StatusServiceUnavailable, now)
	return schedulerBlocked("auth_pool_unavailable", "bound auth pool has no available auth candidates", http.StatusServiceUnavailable)
}

func schedulerPriorityForCandidate(candidate SchedulerAuthCandidate, authTypes map[string]string, typePriorities, overrides map[string]int) int {
	authID := normalizeAuthIDKey(candidate.ID)
	if priority, ok := authIDIntValue(overrides, authID); ok {
		return priority
	}
	if accountTypeValue, ok := authIDStringValue(authTypes, authID); ok {
		accountType := normalizeAccountType(accountTypeValue)
		if priority, ok := typePriorities[accountType]; ok {
			return priority
		}
	}
	matched := false
	priority := candidate.Priority
	for _, accountType := range candidateAccountTypes(candidate) {
		value, ok := typePriorities[normalizeAccountType(accountType)]
		if !ok || (matched && value <= priority) {
			continue
		}
		priority = value
		matched = true
	}
	return priority
}

func authIDStringValue(values map[string]string, authID string) (string, bool) {
	if value, ok := values[authID]; ok {
		return value, true
	}
	for key, value := range values {
		if normalizeAuthIDKey(key) == authID {
			return value, true
		}
	}
	return "", false
}

func authIDIntValue(values map[string]int, authID string) (int, bool) {
	if value, ok := values[authID]; ok {
		return value, true
	}
	for key, value := range values {
		if normalizeAuthIDKey(key) == authID {
			return value, true
		}
	}
	return 0, false
}

func schedulerIneligibleReason(candidates []SchedulerAuthCandidate) string {
	allNegative := len(candidates) > 0
	for _, candidate := range candidates {
		if candidate.Priority >= 0 {
			allNegative = false
			break
		}
	}
	if allNegative {
		return "all_candidates_quota_exhausted"
	}
	return "pool_candidates_unavailable"
}

func schedulerCandidateStatusEligible(status string) bool {
	status = normalizeAccountType(status)
	switch status {
	case "disabled", "error", "expired", "revoked", "invalid", "unavailable", "cooldown", "cooling_down", "quota_exhausted", "exhausted", "blocked":
		return false
	default:
		return true
	}
}

func schedulerCandidateAccountTypes(candidate SchedulerAuthCandidate, authTypes map[string]string) []string {
	values := candidateAccountTypes(candidate)
	if accountType, ok := authIDStringValue(authTypes, normalizeAuthIDKey(candidate.ID)); ok {
		values = append(values, accountTypeAliases(accountType)...)
	}
	return cleanLowerStringList(values)
}

func poolCandidateAuthIDs(pool PoolConfig) []string {
	ids := make([]string, 0, len(pool.AuthIDs)+len(pool.ResolvedAuthIDs))
	ids = append(ids, pool.AuthIDs...)
	ids = append(ids, pool.ResolvedAuthIDs...)
	return ids
}

func normalizePoolSchedulingStrategy(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "round-robin", "round_robin", "roundrobin", "rr":
		return poolSchedulingRoundRobin, true
	case "fill-first", "fill_first", "fillfirst", "ff":
		return poolSchedulingFillFirst, true
	default:
		return "", false
	}
}

func normalizedPoolSchedulingStrategy(value string) string {
	strategy, ok := normalizePoolSchedulingStrategy(value)
	if !ok {
		return poolSchedulingRoundRobin
	}
	return strategy
}

func schedulerCursorKey(poolID, provider, model string, priority int) string {
	return strings.ToLower(strings.TrimSpace(poolID)) + "\x00" +
		strings.ToLower(strings.TrimSpace(provider)) + "\x00" +
		normalizeModelID(model) + "\x00" + strconv.Itoa(priority)
}

func (a *App) nextSchedulerCursor(key string, size int) int {
	if size <= 1 {
		return 0
	}
	a.schedulerMu.Lock()
	defer a.schedulerMu.Unlock()
	if a.schedulerCursors == nil {
		a.schedulerCursors = map[string]int{}
	}
	if _, exists := a.schedulerCursors[key]; !exists && len(a.schedulerCursors) >= 4096 {
		a.schedulerCursors = map[string]int{}
	}
	cursor := a.schedulerCursors[key]
	if cursor >= 2_147_483_640 {
		cursor = 0
	}
	a.schedulerCursors[key] = cursor + 1
	return cursor % size
}

func schedulerBlocked(code, message string, status int) ([]byte, error) {
	return ErrorEnvelope(code, "cpa-auth-pool: "+message, status), nil
}

func explicitCandidateConflictsWithPool(candidate SchedulerAuthCandidate, pool PoolConfig) bool {
	return candidateConflictsWithPoolMode(candidate, pool, true)
}

func candidateConflictsWithPool(candidate SchedulerAuthCandidate, pool PoolConfig) bool {
	return candidateConflictsWithPoolMode(candidate, pool, false)
}

func candidateConflictsWithPoolMode(candidate SchedulerAuthCandidate, pool PoolConfig, allowUnknownTier bool) bool {
	poolTiers := poolStrictCodexTiers(pool)
	if len(poolTiers) == 0 {
		return false
	}
	candidateTiers := candidateDeclaredCodexTiers(candidate)
	if len(candidateTiers) == 0 {
		candidateTiers = candidateInferredCodexTiers(candidate)
	}
	if len(candidateTiers) == 0 {
		return !allowUnknownTier
	}
	for tier := range candidateTiers {
		if _, ok := poolTiers[tier]; ok {
			return false
		}
	}
	return true
}

func poolStrictCodexTiers(pool PoolConfig) map[string]struct{} {
	tiers := map[string]struct{}{}
	for _, accountType := range pool.AccountTypes {
		for _, tier := range strictCodexTiersFromValue(accountType) {
			tiers[tier] = struct{}{}
		}
	}
	return tiers
}

func poolFallbackConcurrencyTier(pool PoolConfig) string {
	tiers := poolStrictCodexTiers(pool)
	if len(tiers) != 1 {
		return ""
	}
	for tier := range tiers {
		return tier
	}
	return ""
}

func candidateDeclaredCodexTiers(candidate SchedulerAuthCandidate) map[string]struct{} {
	tiers := map[string]struct{}{}
	for _, key := range []string{"account_type", "accountType", "plan_type", "tier", "chatgpt_plan_type", "chatgptPlanType", "planType", "type", "kind"} {
		if candidate.Attributes != nil {
			for _, tier := range strictCodexTiersFromValue(candidate.Attributes[key]) {
				tiers[tier] = struct{}{}
			}
		}
		if candidate.Metadata != nil {
			if text, ok := candidate.Metadata[key].(string); ok {
				for _, tier := range strictCodexTiersFromValue(text) {
					tiers[tier] = struct{}{}
				}
			}
		}
	}
	return tiers
}

func candidateInferredCodexTiers(candidate SchedulerAuthCandidate) map[string]struct{} {
	tiers := map[string]struct{}{}
	for _, value := range []string{candidate.Provider, candidate.ID} {
		for _, tier := range strictCodexTiersFromValue(value) {
			tiers[tier] = struct{}{}
		}
	}
	return tiers
}

func poolAccountTypeMatches(value string) []string {
	seen := map[string]struct{}{}
	matches := []string{}
	add := func(candidate string) {
		candidate = normalizeAccountType(candidate)
		if candidate == "" {
			return
		}
		if _, ok := seen[candidate]; ok {
			return
		}
		seen[candidate] = struct{}{}
		matches = append(matches, candidate)
	}
	add(value)
	for _, tier := range strictCodexTiersFromValue(value) {
		add(tier)
	}
	return matches
}

func strictCodexTiersFromValue(value string) []string {
	seen := map[string]struct{}{}
	tiers := []string{}
	add := func(candidate string) {
		if tier := normalizeStrictCodexTier(candidate); tier != "" {
			if _, ok := seen[tier]; ok {
				return
			}
			seen[tier] = struct{}{}
			tiers = append(tiers, tier)
		}
	}
	normalized := normalizeAccountType(value)
	add(normalized)
	for _, part := range strings.FieldsFunc(normalized, func(r rune) bool { return r == '_' }) {
		add(part)
	}
	return tiers
}

func normalizeStrictCodexTier(value string) string {
	switch normalizeConcurrencyTier(value) {
	case "free", "plus", "team", "pro", "enterprise", "business", "edu", "education", "student", "k12":
		return normalizeConcurrencyTier(value)
	default:
		return ""
	}
}

func candidateAccountTypes(candidate SchedulerAuthCandidate) []string {
	seen := map[string]bool{}
	values := []string{}
	add := func(value string) {
		for _, normalized := range accountTypeAliases(value) {
			if normalized == "" || seen[normalized] {
				continue
			}
			seen[normalized] = true
			values = append(values, normalized)
		}
	}
	add(candidate.Provider)
	add(candidate.ID)
	for _, key := range []string{"account_type", "accountType", "plan_type", "tier", "chatgpt_plan_type", "chatgptPlanType", "planType", "provider", "type", "kind", "service", "source"} {
		if candidate.Attributes != nil {
			add(candidate.Attributes[key])
		}
		if candidate.Metadata != nil {
			if text, ok := candidate.Metadata[key].(string); ok {
				add(text)
			}
		}
	}
	if len(values) == 0 {
		values = append(values, "supported")
	}
	return values
}

func accountTypeAliases(value string) []string {
	normalized := normalizeAccountType(value)
	if normalized == "" {
		return nil
	}
	aliases := []string{normalized}
	switch {
	case strings.Contains(normalized, "gemini") || strings.Contains(normalized, "google"):
		aliases = append(aliases, "gemini")
	case strings.Contains(normalized, "grok") || strings.Contains(normalized, "xai") || strings.Contains(normalized, "x_ai"):
		aliases = append(aliases, "grok")
	case strings.Contains(normalized, "claude") || strings.Contains(normalized, "anthropic"):
		aliases = append(aliases, "claude")
	case normalized == "codex" || strings.Contains(normalized, "codex") || strings.Contains(normalized, "chatgpt"):
		aliases = append(aliases, "codex")
	case normalized == "free" || normalized == "plus" || normalized == "team" || normalized == "pro" || normalized == "enterprise" || normalized == "k12" || normalized == "edu" || normalized == "education" || normalized == "student":
		aliases = append(aliases, "codex")
	}
	return aliases
}

func normalizeAccountType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(value, "openai-compatible") || strings.HasPrefix(value, "openai_compatible") || strings.HasPrefix(value, "openai compatible") {
		return "openai_compatible"
	}
	value = strings.NewReplacer("-", "_", " ", "_", ".", "_", "@", "_", "/", "_", "\\", "_").Replace(value)
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '_' })
	cleaned := []string{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			cleaned = append(cleaned, part)
		}
	}
	return strings.Join(cleaned, "_")
}

func normalizeAuthIDKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "\\", "/")
	if index := strings.LastIndex(value, "/"); index >= 0 {
		value = value[index+1:]
	}
	var builder strings.Builder
	lastUnderscore := false
	for _, char := range value {
		isLetter := char >= 'a' && char <= 'z'
		isDigit := char >= '0' && char <= '9'
		if isLetter || isDigit {
			builder.WriteRune(char)
			lastUnderscore = false
			continue
		}
		if builder.Len() > 0 && !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}
	key := strings.Trim(builder.String(), "_")
	for _, prefix := range []string{"root_cli_proxy_api_", "root_cli_proxy_", "cli_proxy_api_"} {
		if strings.HasPrefix(key, prefix) {
			return strings.TrimPrefix(key, prefix)
		}
	}
	return key
}

func (a *App) poolLocked(id string) (PoolConfig, bool) {
	for _, pool := range a.state.Pools {
		if pool.ID == id {
			return pool, true
		}
	}
	return PoolConfig{}, false
}

func defaultStateFile() string {
	return filepath.Join("plugins", legacyStateFile)
}

func resolveStateFile(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == legacyStateFile {
		return defaultStateFile()
	}
	return filepath.Clean(value)
}

func validateStateFilePath(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) {
		return nil
	}
	for _, part := range strings.FieldsFunc(filepath.ToSlash(value), func(char rune) bool { return char == '/' }) {
		if part == ".." {
			return errors.New("state_file must not traverse parent directories")
		}
	}
	return nil
}

func legacyStateCandidates(stateFile string) []string {
	seen := map[string]struct{}{}
	candidates := []string{}
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || path == stateFile {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		candidates = append(candidates, path)
	}
	add(legacyStateFile)
	add(filepath.Join(".", legacyStateFile))
	add(filepath.Join(filepath.Dir(stateFile), legacyStateFile))
	return candidates
}

func migrateLegacyStateFile(stateFile string) error {
	if _, err := os.Stat(stateFile); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, candidate := range legacyStateCandidates(stateFile) {
		raw, err := os.ReadFile(candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(stateFile), 0o700); err != nil && filepath.Dir(stateFile) != "." {
			return err
		}
		return os.WriteFile(stateFile, raw, 0o600)
	}
	return nil
}

func (a *App) load() error {
	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	a.mu.Lock()
	if a.stateFile == "" {
		a.stateFile = defaultStateFile()
	}
	a.stateFile = resolveStateFile(a.stateFile)
	stateFile := a.stateFile
	a.mu.Unlock()
	if err := migrateLegacyStateFile(stateFile); err != nil {
		return err
	}
	raw, err := os.ReadFile(stateFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			a.mu.Lock()
			a.state = State{Pools: []PoolConfig{}, KeyBindings: map[string]KeyBinding{}, AuthModels: map[string][]string{}, AuthTypes: map[string]string{}, TypePriorities: map[string]int{}, AuthPriorityOverrides: map[string]int{}, ProxyKeyHashes: []string{}, CodexConcurrencyLimits: defaultCodexConcurrencyLimits(), ConcurrencySlots: map[string]ConcurrencySlot{}, PoolConcurrencySlots: map[string]PoolConcurrencySlot{}, FailureCooldowns: map[string]FailureCooldown{}, ModelCooldowns: map[string]ModelCooldown{}}
			a.mu.Unlock()
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
	if state.AuthTypes == nil {
		state.AuthTypes = map[string]string{}
	}
	if state.TypePriorities == nil {
		state.TypePriorities = map[string]int{}
	}
	if state.AuthPriorityOverrides == nil {
		state.AuthPriorityOverrides = map[string]int{}
	}
	for index := range state.Pools {
		strategy, ok := normalizePoolSchedulingStrategy(state.Pools[index].SchedulingStrategy)
		if !ok {
			return fmt.Errorf("pool %q has invalid scheduling_strategy %q", state.Pools[index].ID, state.Pools[index].SchedulingStrategy)
		}
		state.Pools[index].SchedulingStrategy = strategy
		if state.Pools[index].MaxConcurrency < 0 || state.Pools[index].MaxConcurrency > 4096 {
			return fmt.Errorf("pool %q has invalid max_concurrency %d", state.Pools[index].ID, state.Pools[index].MaxConcurrency)
		}
	}
	if err := validateLogicalPriorities("type_priorities", state.TypePriorities); err != nil {
		return err
	}
	if err := validateLogicalPriorities("auth_priority_overrides", state.AuthPriorityOverrides); err != nil {
		return err
	}
	state.AuthTypes = normalizeAuthTypes(state.AuthTypes)
	state.TypePriorities = normalizeTypePriorities(state.TypePriorities)
	state.AuthPriorityOverrides = normalizeAuthPriorityOverrides(state.AuthPriorityOverrides)
	if state.ProxyKeyHashes == nil {
		state.ProxyKeyHashes = []string{}
	}
	if state.CodexConcurrencyLimits == nil {
		state.CodexConcurrencyLimits = defaultCodexConcurrencyLimits()
	}
	if state.ConcurrencySlots == nil {
		state.ConcurrencySlots = map[string]ConcurrencySlot{}
	}
	if state.PoolConcurrencySlots == nil {
		state.PoolConcurrencySlots = map[string]PoolConcurrencySlot{}
	}
	if state.FailureCooldowns == nil {
		state.FailureCooldowns = map[string]FailureCooldown{}
	}
	if state.ModelCooldowns == nil {
		state.ModelCooldowns = map[string]ModelCooldown{}
	}
	a.mu.Lock()
	a.state = state
	a.mu.Unlock()
	return nil
}

func (a *App) save() error {
	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	a.mu.RLock()
	state := cloneState(a.state)
	stateFile := a.stateFile
	a.mu.RUnlock()
	if stateFile == "" {
		stateFile = defaultStateFile()
	}
	return persistState(state, stateFile)
}

func (a *App) saveRuntimeState() {
	a.mu.RLock()
	configured := strings.TrimSpace(a.stateFile) != ""
	a.mu.RUnlock()
	if configured {
		_ = a.save()
	}
}

func persistState(state State, stateFile string) error {
	if stateFile == "" {
		stateFile = defaultStateFile()
	}
	stateFile = resolveStateFile(stateFile)
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(stateFile)
	if err := os.MkdirAll(dir, 0o700); err != nil && dir != "." {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(stateFile)+".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, stateFile)
}

func cloneState(state State) State {
	cloned := State{
		Pools:                  make([]PoolConfig, len(state.Pools)),
		KeyBindings:            make(map[string]KeyBinding, len(state.KeyBindings)),
		AuthModels:             make(map[string][]string, len(state.AuthModels)),
		AuthTypes:              cloneStringMap(state.AuthTypes),
		TypePriorities:         cloneIntMap(state.TypePriorities),
		AuthPriorityOverrides:  cloneIntMap(state.AuthPriorityOverrides),
		ProxyKeyHashes:         append([]string(nil), state.ProxyKeyHashes...),
		CodexConcurrencyLimits: make(map[string]int, len(state.CodexConcurrencyLimits)),
		ConcurrencySlots:       make(map[string]ConcurrencySlot, len(state.ConcurrencySlots)),
		PoolConcurrencySlots:   make(map[string]PoolConcurrencySlot, len(state.PoolConcurrencySlots)),
		FailureCooldowns:       cloneFailureCooldownMap(state.FailureCooldowns),
		ModelCooldowns:         cloneModelCooldownMap(state.ModelCooldowns),
	}
	for index, pool := range state.Pools {
		pool.AuthIDs = append([]string(nil), pool.AuthIDs...)
		pool.ResolvedAuthIDs = append([]string(nil), pool.ResolvedAuthIDs...)
		pool.AccountTypes = append([]string(nil), pool.AccountTypes...)
		pool.Providers = append([]string(nil), pool.Providers...)
		pool.Models = append([]string(nil), pool.Models...)
		cloned.Pools[index] = pool
	}
	for hash, binding := range state.KeyBindings {
		cloned.KeyBindings[hash] = binding
	}
	for authID, models := range state.AuthModels {
		cloned.AuthModels[authID] = append([]string(nil), models...)
	}
	for tier, limit := range state.CodexConcurrencyLimits {
		cloned.CodexConcurrencyLimits[tier] = limit
	}
	for authID, slot := range state.ConcurrencySlots {
		cloned.ConcurrencySlots[authID] = slot
	}
	for poolID, slot := range state.PoolConcurrencySlots {
		cloned.PoolConcurrencySlots[poolID] = slot
	}
	return cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneIntMap(values map[string]int) map[string]int {
	cloned := make(map[string]int, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
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

func extractHelperAPIKeyHash(headers map[string][]string) string {
	for name, values := range headers {
		if len(values) == 0 || !strings.EqualFold(name, helperAPIKeyHashHeader) {
			continue
		}
		return strings.ToLower(strings.TrimSpace(values[0]))
	}
	return ""
}

func (a *App) isTrustedProxyAPIKey(apiKey string) bool {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return false
	}
	apiKeyHash := hashAPIKey(apiKey)
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, trusted := range a.state.ProxyKeyHashes {
		if strings.EqualFold(strings.TrimSpace(trusted), apiKeyHash) {
			return true
		}
	}
	return false
}
