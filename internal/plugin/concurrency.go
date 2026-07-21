package plugin

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	defaultConcurrencySlotTTL = 10 * time.Minute
	defaultRateLimitCooldown  = 2 * time.Minute
	defaultUsageLimitCooldown = 30 * time.Minute
	defaultNetworkCooldown    = 15 * time.Second
	defaultModelCooldown      = 30 * time.Minute
	maximumFailureCooldown    = 365 * 24 * time.Hour
)

type ConcurrencySlot struct {
	AuthID    string    `json:"auth_id"`
	Tier      string    `json:"tier"`
	Count     int       `json:"count"`
	StartedAt time.Time `json:"started_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type FailureCooldown struct {
	AuthID    string    `json:"auth_id"`
	ErrorCode string    `json:"error_code"`
	Message   string    `json:"message,omitempty"`
	Until     time.Time `json:"until"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PoolConcurrencySlot struct {
	PoolID    string    `json:"pool_id"`
	Count     int       `json:"count"`
	StartedAt time.Time `json:"started_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type ModelCooldown struct {
	AuthID    string    `json:"auth_id"`
	Model     string    `json:"model"`
	Until     time.Time `json:"until"`
	UpdatedAt time.Time `json:"updated_at"`
}

type concurrencyPickResult struct {
	Candidate SchedulerAuthCandidate
	Selected  bool
	Blocked   bool
}

func (a *App) handleUsage(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return OKEnvelope(map[string]any{})
	}
	var record UsageRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return OKEnvelope(map[string]any{})
	}
	result := a.recordUsageEvent(record)
	if authID := strings.TrimSpace(record.AuthID); authID != "" && !record.Additional {
		poolID := ""
		if result.Attributed {
			poolID = result.Attribution.PoolID
		}
		a.releaseConcurrencySlot(authID, poolID)
		now := time.Now()
		changed := a.updateFailureCooldown(authID, record, result.Failure, now)
		changed = a.updateModelCooldown(authID, record, result.Failure, now) || changed
		if changed {
			a.saveRuntimeState()
		}
	}
	return OKEnvelope(map[string]any{})
}

func (a *App) updateFailureCooldown(authID string, record UsageRecord, failure pluginUsageFailure, now time.Time) bool {
	authID = strings.TrimSpace(authID)
	key := normalizeAuthIDKey(authID)
	if key == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.FailureCooldowns == nil {
		a.state.FailureCooldowns = map[string]FailureCooldown{}
	}
	if !record.Failed {
		if _, exists := a.state.FailureCooldowns[key]; exists {
			delete(a.state.FailureCooldowns, key)
			return true
		}
		return false
	}
	if record.Failure.StatusCode != 429 && failure.Code != "usage_limit_reached" && failure.Code != "rate_limited" && !isNetworkFailureCode(failure.Code) {
		return false
	}
	until := failureCooldownUntil(failure, now)
	next := FailureCooldown{AuthID: authID, ErrorCode: failure.Code, Message: failure.Message, Until: until, UpdatedAt: now}
	current, exists := a.state.FailureCooldowns[key]
	if exists && current.ErrorCode == next.ErrorCode && current.Message == next.Message && current.Until.Equal(next.Until) {
		return false
	}
	a.state.FailureCooldowns[key] = next
	return true
}

func failureCooldownUntil(failure pluginUsageFailure, now time.Time) time.Time {
	if failure.ResetsAt > 0 {
		until := time.Unix(failure.ResetsAt, 0)
		if until.After(now) && until.Sub(now) <= maximumFailureCooldown {
			return until
		}
	}
	if failure.ResetsInSeconds > 0 {
		seconds := failure.ResetsInSeconds
		maxSeconds := int64(maximumFailureCooldown / time.Second)
		if seconds > maxSeconds {
			seconds = maxSeconds
		}
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if failure.Code == "usage_limit_reached" {
		return now.Add(defaultUsageLimitCooldown)
	}
	if isNetworkFailureCode(failure.Code) {
		return now.Add(defaultNetworkCooldown)
	}
	return now.Add(defaultRateLimitCooldown)
}

func isNetworkFailureCode(code string) bool {
	switch code {
	case "proxy_network_unreachable", "proxy_connection_refused", "proxy_timeout", "proxy_dns_failed", "proxy_connect_failed",
		"network_unreachable", "connection_refused", "connection_reset", "network_timeout", "dns_failed", "tls_failed", "network_eof", "network_broken_pipe":
		return true
	default:
		return false
	}
}

func modelCooldownKey(authID, model string) string {
	return normalizeAuthIDKey(authID) + "\x00" + normalizeModelID(model)
}

func modelCooldownActive(cooldowns map[string]ModelCooldown, authID, model string, now time.Time) bool {
	if normalizeModelID(model) == "" {
		return false
	}
	cooldown, ok := cooldowns[modelCooldownKey(authID, model)]
	return ok && cooldown.Until.After(now)
}

func cloneModelCooldownMap(values map[string]ModelCooldown) map[string]ModelCooldown {
	cloned := make(map[string]ModelCooldown, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func (a *App) updateModelCooldown(authID string, record UsageRecord, failure pluginUsageFailure, now time.Time) bool {
	model := normalizeModelID(record.Model)
	key := modelCooldownKey(authID, model)
	if normalizeAuthIDKey(authID) == "" || model == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.ModelCooldowns == nil {
		a.state.ModelCooldowns = map[string]ModelCooldown{}
	}
	if !record.Failed {
		if _, exists := a.state.ModelCooldowns[key]; exists {
			delete(a.state.ModelCooldowns, key)
			return true
		}
		return false
	}
	if failure.Code != "model_not_supported" {
		return false
	}
	a.state.ModelCooldowns[key] = ModelCooldown{
		AuthID: strings.TrimSpace(authID), Model: strings.TrimSpace(record.Model),
		Until: now.Add(defaultModelCooldown), UpdatedAt: now,
	}
	return true
}

func (a *App) clearExpiredModelCooldowns(now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	changed := false
	for key, cooldown := range a.state.ModelCooldowns {
		if cooldown.Until.IsZero() || !cooldown.Until.After(now) {
			delete(a.state.ModelCooldowns, key)
			changed = true
		}
	}
	return changed
}

func failureCooldownActive(cooldowns map[string]FailureCooldown, authID string, now time.Time) bool {
	cooldown, ok := cooldowns[normalizeAuthIDKey(authID)]
	return ok && cooldown.Until.After(now)
}

func cloneFailureCooldownMap(values map[string]FailureCooldown) map[string]FailureCooldown {
	cloned := make(map[string]FailureCooldown, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func (a *App) clearExpiredFailureCooldowns(now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	changed := false
	for key, cooldown := range a.state.FailureCooldowns {
		if cooldown.Until.IsZero() || !cooldown.Until.After(now) {
			delete(a.state.FailureCooldowns, key)
			changed = true
		}
	}
	return changed
}

func (a *App) clearExpiredConcurrencySlots(now time.Time) {
	a.mu.Lock()
	for authID, slot := range a.state.ConcurrencySlots {
		if slot.ExpiresAt.IsZero() || !now.Before(slot.ExpiresAt) {
			delete(a.state.ConcurrencySlots, authID)
		}
	}
	for poolID, slot := range a.state.PoolConcurrencySlots {
		if slot.ExpiresAt.IsZero() || !now.Before(slot.ExpiresAt) {
			delete(a.state.PoolConcurrencySlots, poolID)
		}
	}
	a.mu.Unlock()
}

func (a *App) releaseConcurrencySlot(authID string, poolIDs ...string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	a.mu.Lock()
	slot, existed := a.state.ConcurrencySlots[authID]
	if existed && slot.Count <= 1 {
		delete(a.state.ConcurrencySlots, authID)
	} else if existed {
		slot.Count--
		a.state.ConcurrencySlots[authID] = slot
	}
	poolID := ""
	if len(poolIDs) > 0 {
		poolID = strings.TrimSpace(poolIDs[0])
	}
	if poolID != "" {
		poolSlot, poolExisted := a.state.PoolConcurrencySlots[poolID]
		if poolExisted && poolSlot.Count <= 1 {
			delete(a.state.PoolConcurrencySlots, poolID)
		} else if poolExisted {
			poolSlot.Count--
			a.state.PoolConcurrencySlots[poolID] = poolSlot
		}
	}
	a.mu.Unlock()
}

// selectAndReserveConcurrencyCandidate reserves a candidate atomically. Fill-first
// keeps using the first candidate with capacity; round-robin chooses the least
// loaded candidate and uses the offset to break load ties.
func (a *App) selectAndReserveConcurrencyCandidate(candidates []SchedulerAuthCandidate, tiers map[string]string, pool PoolConfig, offset int, strategy string, now time.Time) concurrencyPickResult {
	if len(candidates) == 0 {
		return concurrencyPickResult{}
	}
	if offset < 0 {
		offset = 0
	}
	offset %= len(candidates)

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.ConcurrencySlots == nil {
		a.state.ConcurrencySlots = map[string]ConcurrencySlot{}
	}
	if a.state.PoolConcurrencySlots == nil {
		a.state.PoolConcurrencySlots = map[string]PoolConcurrencySlot{}
	}
	poolSlot := a.state.PoolConcurrencySlots[pool.ID]
	if poolSlot.ExpiresAt.IsZero() || !now.Before(poolSlot.ExpiresAt) {
		delete(a.state.PoolConcurrencySlots, pool.ID)
		poolSlot = PoolConcurrencySlot{}
	}
	poolCount := poolSlot.Count
	if poolCount <= 0 && !poolSlot.ExpiresAt.IsZero() {
		poolCount = 1
	}
	if pool.MaxConcurrency > 0 && poolCount >= pool.MaxConcurrency {
		return concurrencyPickResult{Blocked: true}
	}

	selectedIndex := -1
	selectedCount := 0
	blocked := false
	fillFirst := normalizedPoolSchedulingStrategy(strategy) == poolSchedulingFillFirst
	for step := 0; step < len(candidates); step++ {
		index := (offset + step) % len(candidates)
		candidate := candidates[index]
		tier := normalizeConcurrencyTier(tiers[candidate.ID])
		if tier == "" {
			if fillFirst {
				selectedIndex = index
				break
			}
			// Non-Codex candidates do not consume Codex concurrency slots.
			if selectedIndex < 0 || selectedCount > 0 {
				selectedIndex = index
				selectedCount = 0
			}
			continue
		}

		slot := a.state.ConcurrencySlots[candidate.ID]
		if slot.ExpiresAt.IsZero() || !now.Before(slot.ExpiresAt) {
			delete(a.state.ConcurrencySlots, candidate.ID)
			slot = ConcurrencySlot{}
		}
		count := slot.Count
		if count <= 0 && !slot.ExpiresAt.IsZero() {
			count = 1
		}
		limit := a.codexConcurrencyLimitLocked(tier)
		if limit > 0 && count >= limit {
			blocked = true
			continue
		}
		if fillFirst {
			selectedIndex = index
			selectedCount = count
			break
		}
		if selectedIndex < 0 || count < selectedCount {
			selectedIndex = index
			selectedCount = count
		}
	}
	if selectedIndex < 0 {
		return concurrencyPickResult{Blocked: blocked}
	}

	selected := candidates[selectedIndex]
	if tier := normalizeConcurrencyTier(tiers[selected.ID]); tier != "" {
		a.reserveConcurrencySlotLocked(selected, tier, now)
	}
	a.reservePoolConcurrencySlotLocked(pool.ID, now)
	return concurrencyPickResult{Candidate: selected, Selected: true, Blocked: blocked}
}

func (a *App) reservePoolConcurrencySlotLocked(poolID string, now time.Time) {
	poolID = strings.TrimSpace(poolID)
	if poolID == "" {
		return
	}
	if a.state.PoolConcurrencySlots == nil {
		a.state.PoolConcurrencySlots = map[string]PoolConcurrencySlot{}
	}
	slot := a.state.PoolConcurrencySlots[poolID]
	if slot.Count <= 0 {
		slot.StartedAt = now
	}
	slot.PoolID = poolID
	slot.Count++
	slot.ExpiresAt = now.Add(defaultConcurrencySlotTTL)
	a.state.PoolConcurrencySlots[poolID] = slot
}

func (a *App) reserveConcurrencySlotLocked(candidate SchedulerAuthCandidate, tier string, now time.Time) {
	if a.state.ConcurrencySlots == nil {
		a.state.ConcurrencySlots = map[string]ConcurrencySlot{}
	}
	slot := a.state.ConcurrencySlots[candidate.ID]
	if slot.Count <= 0 {
		slot.StartedAt = now
	}
	slot.AuthID = candidate.ID
	slot.Tier = tier
	slot.Count++
	slot.ExpiresAt = now.Add(defaultConcurrencySlotTTL)
	a.state.ConcurrencySlots[candidate.ID] = slot
}

func (a *App) codexConcurrencyLimitLocked(tier string) int {
	limit := a.state.CodexConcurrencyLimits[normalizeConcurrencyTier(tier)]
	if limit > 0 {
		return limit
	}
	limit = a.state.CodexConcurrencyLimits["default"]
	if limit > 0 {
		return limit
	}
	return 0
}

func (a *App) codexConcurrencyCountsLocked(now time.Time) map[string]int {
	counts := map[string]int{}
	for _, slot := range a.state.ConcurrencySlots {
		if slot.ExpiresAt.IsZero() || !now.Before(slot.ExpiresAt) {
			continue
		}
		tier := normalizeConcurrencyTier(slot.Tier)
		if tier == "" {
			continue
		}
		count := slot.Count
		if count <= 0 {
			count = 1
		}
		counts[tier] += count
	}
	return counts
}

func candidateCodexConcurrencyTier(candidate SchedulerAuthCandidate) (string, bool) {
	types := candidateAccountTypes(candidate)
	isCodex := false
	bestTier := ""
	for _, candidateType := range types {
		candidateType = normalizeConcurrencyTier(candidateType)
		if candidateType == "" || candidateType == "openai_compatible" {
			continue
		}
		if candidateType == "codex" || strings.Contains(candidateType, "chatgpt") {
			isCodex = true
			if bestTier == "" {
				bestTier = "default"
			}
			continue
		}
		if isCodexTier(candidateType) {
			isCodex = true
			if bestTier == "" || bestTier == "default" || bestTier == "codex" {
				bestTier = candidateType
			}
		}
	}
	if !isCodex {
		return "", false
	}
	if bestTier == "" || bestTier == "codex" {
		bestTier = "default"
	}
	return bestTier, true
}

func isCodexTier(value string) bool {
	switch normalizeConcurrencyTier(value) {
	case "free", "plus", "team", "pro", "enterprise", "business", "edu":
		return true
	default:
		return false
	}
}

func normalizeConcurrencyTier(value string) string {
	value = normalizeAccountType(value)
	switch value {
	case "chatgpt_free", "codex_free", "openai_free":
		return "free"
	case "chatgpt_plus", "codex_plus", "openai_plus":
		return "plus"
	case "chatgpt_team", "codex_team", "openai_team":
		return "team"
	case "chatgpt_pro", "codex_pro", "openai_pro":
		return "pro"
	case "chatgpt_enterprise", "codex_enterprise", "openai_enterprise":
		return "enterprise"
	case "chatgpt_business", "codex_business", "openai_business":
		return "business"
	case "":
		return ""
	default:
		return value
	}
}

func defaultCodexConcurrencyLimits() map[string]int {
	return map[string]int{"default": 0}
}
