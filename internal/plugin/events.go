package plugin

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	pluginEventCapacity        = 500
	pluginEventDefaultLimit    = 100
	pluginEventCandidateLimit  = 25
	pluginEventReasonLimit     = 320
	pluginEventDetailLimit     = 2048
	pluginEventReasonScanLimit = 4096
)

var (
	pluginEventBearerPattern  = regexp.MustCompile(`(?i)(bearer\s+)[a-z0-9._~+/=-]+`)
	pluginEventSecretPattern  = regexp.MustCompile(`(?i)((?:api[_-]?key|token|secret|password|cookie|authorization)\s*[:=]\s*)[^\s,;]+`)
	pluginEventURLUserPattern = regexp.MustCompile(`(?i)(https?|socks5h?)://[^/@\s]+@`)
)

type PluginEvent struct {
	ID               uint64                 `json:"id"`
	Timestamp        time.Time              `json:"timestamp"`
	Phase            string                 `json:"phase"`
	Status           string                 `json:"status"`
	Reason           string                 `json:"reason,omitempty"`
	ErrorCode        string                 `json:"error_code,omitempty"`
	ErrorMessage     string                 `json:"error_message,omitempty"`
	ErrorDetail      string                 `json:"error_detail,omitempty"`
	PlanType         string                 `json:"plan_type,omitempty"`
	ResetsAt         int64                  `json:"resets_at,omitempty"`
	ResetsInSeconds  int64                  `json:"resets_in_seconds,omitempty"`
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
	SelectedPriority *int                   `json:"selected_priority,omitempty"`
	SelectedState    string                 `json:"selected_state,omitempty"`
	CandidateCount   int                    `json:"candidate_count"`
	MatchedCount     int                    `json:"matched_count"`
	InputCandidates  int                    `json:"input_candidates"`
	PoolMatched      int                    `json:"pool_matched_candidates"`
	Eligible         int                    `json:"eligible_candidates"`
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
	a.recordSchedulerEventDetails(req, binding, pool, matched, matched, selected, status, reason, httpStatus, startedAt)
}

func (a *App) recordSchedulerEventDetails(req SchedulerPickRequest, binding *KeyBinding, pool *PoolConfig, poolMatched, eligible []SchedulerAuthCandidate, selected *SchedulerAuthCandidate, status, reason string, httpStatus int, startedAt time.Time) {
	event := PluginEvent{
		Timestamp:       time.Now(),
		Phase:           "selection",
		Status:          status,
		Reason:          truncatePluginEventReason(reason),
		HTTPStatus:      httpStatus,
		DurationMS:      time.Since(startedAt).Milliseconds(),
		Provider:        strings.TrimSpace(req.Provider),
		Model:           strings.TrimSpace(req.Model),
		Stream:          req.Stream,
		CandidateCount:  len(req.Candidates),
		MatchedCount:    len(poolMatched),
		InputCandidates: len(req.Candidates),
		PoolMatched:     len(poolMatched),
		Eligible:        len(eligible),
		MatchedAuthIDs:  candidateIDs(poolMatched, pluginEventCandidateLimit),
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
		priority := selected.Priority
		event.SelectedPriority = &priority
		event.SelectedState = strings.TrimSpace(selected.Status)
		event.Candidates = schedulerCandidateEvents([]SchedulerAuthCandidate{*selected}, 1)
	} else if status == "blocked" {
		event.Candidates = schedulerCandidateEvents(req.Candidates, pluginEventCandidateLimit)
	}
	a.recordPluginEvent(event)
}

func (a *App) recordUsageEvent(record UsageRecord) pluginUsageFailure {
	failure := classifyPluginUsageFailure(record.Failure)
	authID := strings.TrimSpace(record.AuthID)
	if authID == "" {
		return failure
	}
	poolIDs, poolNames := a.poolLabelsForAuthID(authID)
	if len(poolIDs) == 0 {
		return failure
	}
	status := "success"
	if record.Failed {
		status = "failed"
	}
	a.recordPluginEvent(PluginEvent{
		Timestamp:       time.Now(),
		Phase:           "completion",
		Status:          status,
		Reason:          truncatePluginEventReason(record.Failure.Body),
		ErrorCode:       failure.Code,
		ErrorMessage:    failure.Message,
		ErrorDetail:     failure.Detail,
		PlanType:        failure.PlanType,
		ResetsAt:        failure.ResetsAt,
		ResetsInSeconds: failure.ResetsInSeconds,
		HTTPStatus:      record.Failure.StatusCode,
		Provider:        strings.TrimSpace(record.Provider),
		Model:           strings.TrimSpace(record.Model),
		PoolID:          strings.Join(poolIDs, ","),
		PoolName:        strings.Join(poolNames, ","),
		SelectedAuthID:  authID,
	})
	return failure
}

type pluginUsageFailure struct {
	Code            string
	Message         string
	Detail          string
	PlanType        string
	ResetsAt        int64
	ResetsInSeconds int64
}

func classifyPluginUsageFailure(failure UsageFailure) pluginUsageFailure {
	body := strings.TrimSpace(failure.Body)
	if failure.StatusCode == 0 && body == "" {
		return pluginUsageFailure{}
	}
	result := pluginUsageFailure{Detail: sanitizePluginEventText(body, pluginEventDetailLimit)}
	var payload map[string]any
	if json.Unmarshal([]byte(body), &payload) == nil {
		if detail, ok := payload["detail"].(string); ok {
			result.Message = sanitizePluginEventText(detail, pluginEventReasonLimit)
		}
		if rawError, ok := payload["error"].(map[string]any); ok {
			result.Code = normalizePluginErrorCode(pluginEventString(rawError["type"]))
			result.Message = sanitizePluginEventText(pluginEventString(rawError["message"]), pluginEventReasonLimit)
			result.PlanType = sanitizePluginEventText(pluginEventString(rawError["plan_type"]), 64)
			result.ResetsAt = pluginEventInt64(rawError["resets_at"])
			result.ResetsInSeconds = pluginEventInt64(rawError["resets_in_seconds"])
		}
	}

	lowerBody := strings.ToLower(body)
	lowerMessage := strings.ToLower(result.Message)
	switch {
	case strings.Contains(lowerMessage, "model is not supported") ||
		strings.Contains(lowerMessage, "model is unsupported") ||
		strings.Contains(lowerBody, "model is not supported"):
		result.Code = "model_not_supported"
	case strings.Contains(lowerBody, "socks connect") && strings.Contains(lowerBody, "network unreachable"):
		result.Code = "proxy_network_unreachable"
	case strings.Contains(lowerBody, "socks connect") && strings.Contains(lowerBody, "connection refused"):
		result.Code = "proxy_connection_refused"
	case strings.Contains(lowerBody, "socks connect") && (strings.Contains(lowerBody, "i/o timeout") || strings.Contains(lowerBody, "deadline exceeded")):
		result.Code = "proxy_timeout"
	case strings.Contains(lowerBody, "socks connect") && strings.Contains(lowerBody, "no such host"):
		result.Code = "proxy_dns_failed"
	case strings.Contains(lowerBody, "socks connect") && result.Code == "":
		result.Code = "proxy_connect_failed"
	}

	if result.Code == "" {
		switch failure.StatusCode {
		case http.StatusBadRequest:
			result.Code = "upstream_bad_request"
		case http.StatusTooManyRequests:
			result.Code = "rate_limited"
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			result.Code = "upstream_unavailable"
		}
	}
	if result.Message == "" {
		result.Message = pluginUsageFailureMessage(result.Code)
	}
	return result
}

func pluginUsageFailureMessage(code string) string {
	switch code {
	case "model_not_supported":
		return "the selected account does not support the requested model"
	case "proxy_network_unreachable":
		return "the SOCKS proxy cannot reach the upstream service"
	case "proxy_connection_refused":
		return "the SOCKS proxy connection was refused"
	case "proxy_timeout":
		return "the SOCKS proxy connection timed out"
	case "proxy_dns_failed":
		return "the SOCKS proxy could not resolve the upstream host"
	case "proxy_connect_failed":
		return "the SOCKS proxy connection failed"
	case "usage_limit_reached":
		return "the account usage limit has been reached"
	case "rate_limited":
		return "the upstream service rate limited the request"
	case "upstream_bad_request":
		return "the upstream service rejected the request"
	case "upstream_unavailable":
		return "the upstream service is unavailable"
	default:
		return ""
	}
}

func normalizePluginErrorCode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastUnderscore := false
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			builder.WriteRune(char)
			lastUnderscore = false
			continue
		}
		if builder.Len() > 0 && !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(builder.String(), "_")
}

func pluginEventString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func pluginEventInt64(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed
	default:
		return 0
	}
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
	return sanitizePluginEventText(value, pluginEventReasonLimit)
}

func sanitizePluginEventText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > pluginEventReasonScanLimit {
		value = value[:pluginEventReasonScanLimit]
	}
	var payload any
	if json.Unmarshal([]byte(value), &payload) == nil {
		if sanitized, err := json.Marshal(redactPluginEventValue(payload)); err == nil {
			value = string(sanitized)
		}
	}
	value = pluginEventBearerPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = pluginEventSecretPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = pluginEventURLUserPattern.ReplaceAllString(value, `${1}://[REDACTED]@`)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func redactPluginEventValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			if pluginEventSensitiveField(key) {
				result[key] = "[REDACTED]"
				continue
			}
			result[key] = redactPluginEventValue(child)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = redactPluginEventValue(child)
		}
		return result
	default:
		return value
	}
}

func pluginEventSensitiveField(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(key)
	for _, fragment := range []string{"api_key", "apikey", "token", "secret", "password", "cookie", "authorization", "credential"} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}
