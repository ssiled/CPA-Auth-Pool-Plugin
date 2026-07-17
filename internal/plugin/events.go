package plugin

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	pluginEventCapacity       = 500
	pluginEventDefaultLimit   = 100
	pluginEventCandidateLimit = 25
	pluginEventReasonLimit    = 320
)

type PluginEvent struct {
	ID               uint64                 `json:"id"`
	Timestamp        time.Time              `json:"timestamp"`
	Phase            string                 `json:"phase"`
	Status           string                 `json:"status"`
	Reason           string                 `json:"reason,omitempty"`
	HTTPStatus       int                    `json:"http_status,omitempty"`
	DurationMS       int64                  `json:"duration_ms,omitempty"`
	Provider         string                 `json:"provider,omitempty"`
	Model            string                 `json:"model,omitempty"`
	Stream           bool                   `json:"stream,omitempty"`
	PoolID           string                 `json:"pool_id,omitempty"`
	PoolName         string                 `json:"pool_name,omitempty"`
	UserID           int                    `json:"user_id,omitempty"`
	Username         string                 `json:"username,omitempty"`
	SelectedAuthID   string                 `json:"selected_auth_id,omitempty"`
	SelectedPriority int                    `json:"selected_priority,omitempty"`
	SelectedState    string                 `json:"selected_state,omitempty"`
	CandidateCount   int                    `json:"candidate_count"`
	MatchedCount     int                    `json:"matched_count"`
	MatchedAuthIDs   []string               `json:"matched_auth_ids,omitempty"`
	AccountTypes     []string               `json:"account_types,omitempty"`
	Candidates       []PluginEventCandidate `json:"candidates,omitempty"`
}

type PluginEventCandidate struct {
	ID           string   `json:"id"`
	Provider     string   `json:"provider,omitempty"`
	Priority     int      `json:"priority,omitempty"`
	Status       string   `json:"status,omitempty"`
	AccountTypes []string `json:"account_types,omitempty"`
}

type pluginEventResponse struct {
	Items    []PluginEvent `json:"items"`
	Total    int           `json:"total"`
	Capacity int           `json:"capacity"`
}

func (a *App) recordSchedulerEvent(req SchedulerPickRequest, binding *KeyBinding, pool *PoolConfig, matched []SchedulerAuthCandidate, selected *SchedulerAuthCandidate, status, reason string, httpStatus int, startedAt time.Time) {
	event := PluginEvent{
		Timestamp:      time.Now(),
		Phase:          "selection",
		Status:         status,
		Reason:         truncatePluginEventReason(reason),
		HTTPStatus:     httpStatus,
		DurationMS:     time.Since(startedAt).Milliseconds(),
		Provider:       strings.TrimSpace(req.Provider),
		Model:          strings.TrimSpace(req.Model),
		Stream:         req.Stream,
		CandidateCount: len(req.Candidates),
		MatchedCount:   len(matched),
		MatchedAuthIDs: candidateIDs(matched, pluginEventCandidateLimit),
	}
	if binding != nil {
		event.UserID = binding.UserID
		event.Username = strings.TrimSpace(binding.Username)
	}
	if pool != nil {
		event.PoolID = strings.TrimSpace(pool.ID)
		event.PoolName = strings.TrimSpace(pool.Name)
		event.AccountTypes = append([]string(nil), pool.AccountTypes...)
	}
	if selected != nil {
		event.SelectedAuthID = strings.TrimSpace(selected.ID)
		event.SelectedPriority = selected.Priority
		event.SelectedState = strings.TrimSpace(selected.Status)
		event.Candidates = schedulerCandidateEvents([]SchedulerAuthCandidate{*selected}, 1)
	} else if status == "blocked" {
		event.Candidates = schedulerCandidateEvents(req.Candidates, pluginEventCandidateLimit)
	}
	a.recordPluginEvent(event)
}

func (a *App) recordUsageEvent(record UsageRecord) {
	authID := strings.TrimSpace(record.AuthID)
	if authID == "" {
		return
	}
	poolIDs, poolNames := a.poolLabelsForAuthID(authID)
	if len(poolIDs) == 0 {
		return
	}
	status := "success"
	if record.Failed {
		status = "failed"
	}
	a.recordPluginEvent(PluginEvent{
		Timestamp:      time.Now(),
		Phase:          "completion",
		Status:         status,
		Reason:         truncatePluginEventReason(record.Failure.Body),
		HTTPStatus:     record.Failure.StatusCode,
		Provider:       strings.TrimSpace(record.Provider),
		PoolID:         strings.Join(poolIDs, ","),
		PoolName:       strings.Join(poolNames, ","),
		SelectedAuthID: authID,
	})
}

func (a *App) poolLabelsForAuthID(authID string) ([]string, []string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	poolIDs := []string{}
	poolNames := []string{}
	for _, pool := range a.state.Pools {
		for _, candidateID := range poolCandidateAuthIDs(pool) {
			if strings.TrimSpace(candidateID) != authID {
				continue
			}
			poolIDs = append(poolIDs, pool.ID)
			poolNames = append(poolNames, pool.Name)
			break
		}
	}
	return poolIDs, poolNames
}

func (a *App) recordPluginEvent(event PluginEvent) {
	a.eventsMu.Lock()
	defer a.eventsMu.Unlock()
	a.nextEventID++
	event.ID = a.nextEventID
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	if len(a.events) >= pluginEventCapacity {
		a.events[a.eventStart] = event
		a.eventStart = (a.eventStart + 1) % pluginEventCapacity
		return
	}
	a.events = append(a.events, event)
}

func (a *App) pluginEventSnapshot(limit int) pluginEventResponse {
	if limit <= 0 {
		limit = pluginEventDefaultLimit
	}
	if limit > pluginEventCapacity {
		limit = pluginEventCapacity
	}
	a.eventsMu.RLock()
	defer a.eventsMu.RUnlock()
	count := len(a.events)
	if limit > count {
		limit = count
	}
	items := make([]PluginEvent, 0, limit)
	for offset := 0; offset < limit; offset++ {
		index := (a.eventStart + count - 1 - offset) % count
		event := a.events[index]
		event.MatchedAuthIDs = append([]string(nil), event.MatchedAuthIDs...)
		event.AccountTypes = append([]string(nil), event.AccountTypes...)
		event.Candidates = append([]PluginEventCandidate(nil), event.Candidates...)
		items = append(items, event)
	}
	return pluginEventResponse{Items: items, Total: count, Capacity: pluginEventCapacity}
}

func (a *App) clearPluginEvents() int {
	a.eventsMu.Lock()
	defer a.eventsMu.Unlock()
	cleared := len(a.events)
	clear(a.events)
	a.events = a.events[:0]
	a.eventStart = 0
	return cleared
}

func eventLimitFromRequest(req ManagementRequest) int {
	limit, _ := strconv.Atoi(strings.TrimSpace(req.Query.Get("limit")))
	return limit
}

func schedulerCandidateEvents(candidates []SchedulerAuthCandidate, limit int) []PluginEventCandidate {
	if limit <= 0 || limit > len(candidates) {
		limit = len(candidates)
	}
	items := make([]PluginEventCandidate, 0, limit)
	for _, candidate := range candidates[:limit] {
		accountTypes := candidateAccountTypes(candidate)
		sort.Strings(accountTypes)
		items = append(items, PluginEventCandidate{
			ID:           strings.TrimSpace(candidate.ID),
			Provider:     strings.TrimSpace(candidate.Provider),
			Priority:     candidate.Priority,
			Status:       strings.TrimSpace(candidate.Status),
			AccountTypes: accountTypes,
		})
	}
	return items
}

func candidateIDs(candidates []SchedulerAuthCandidate, limit int) []string {
	if limit <= 0 || limit > len(candidates) {
		limit = len(candidates)
	}
	ids := make([]string, 0, limit)
	for _, candidate := range candidates[:limit] {
		if id := strings.TrimSpace(candidate.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func truncatePluginEventReason(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= pluginEventReasonLimit {
		return value
	}
	return value[:pluginEventReasonLimit]
}
