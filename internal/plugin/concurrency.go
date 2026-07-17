package plugin

import (
	"encoding/json"
	"strings"
	"time"
)

const defaultConcurrencySlotTTL = 10 * time.Minute

type ConcurrencySlot struct {
	AuthID    string    `json:"auth_id"`
	Tier      string    `json:"tier"`
	Count     int       `json:"count"`
	StartedAt time.Time `json:"started_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (a *App) handleUsage(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return OKEnvelope(map[string]any{})
	}
	var record UsageRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return OKEnvelope(map[string]any{})
	}
	if authID := strings.TrimSpace(record.AuthID); authID != "" {
		a.releaseConcurrencySlot(authID)
	}
	return OKEnvelope(map[string]any{})
}

func (a *App) clearExpiredConcurrencySlots(now time.Time) {
	a.mu.Lock()
	for authID, slot := range a.state.ConcurrencySlots {
		if slot.ExpiresAt.IsZero() || !now.Before(slot.ExpiresAt) {
			delete(a.state.ConcurrencySlots, authID)
		}
	}
	a.mu.Unlock()
}

func (a *App) releaseConcurrencySlot(authID string) {
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
	a.mu.Unlock()
}

func (a *App) reserveConcurrencySlot(candidate SchedulerAuthCandidate, tier string, now time.Time) {
	tier = normalizeConcurrencyTier(tier)
	if tier == "" {
		return
	}
	a.mu.Lock()
	a.reserveConcurrencySlotLocked(candidate, tier, now)
	a.mu.Unlock()
}

func (a *App) reserveConcurrencySlotIfAvailable(candidate SchedulerAuthCandidate, tier string, now time.Time) bool {
	tier = normalizeConcurrencyTier(tier)
	if tier == "" {
		return true
	}
	a.mu.Lock()
	limit := a.codexConcurrencyLimitLocked(tier)
	counts := a.codexConcurrencyCountsLocked(now)
	if limit > 0 && counts[tier] >= limit {
		a.mu.Unlock()
		return false
	}
	a.reserveConcurrencySlotLocked(candidate, tier, now)
	a.mu.Unlock()
	return true
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

func (a *App) candidateConcurrencyBlocked(candidate SchedulerAuthCandidate, counts map[string]int) (string, bool) {
	tier, isCodex := candidateCodexConcurrencyTier(candidate)
	if !isCodex || tier == "" {
		return "", false
	}
	limit := a.codexConcurrencyLimitLocked(tier)
	if limit <= 0 {
		return tier, false
	}
	return tier, counts[normalizeConcurrencyTier(tier)] >= limit
}
