package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

func (a *App) interceptResponse(raw []byte) ([]byte, error) {
	var req ResponseInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if req.Stream || req.StatusCode < 200 || req.StatusCode >= 300 || !isModelsEndpoint(req) || len(req.Body) == 0 {
		return OKEnvelope(ResponseInterceptResponse{})
	}
	apiKey := extractAPIKeyFromHTTPHeader(req.RequestHeaders)
	apiKeyHash := extractHelperAPIKeyHashFromHTTPHeader(req.RequestHeaders)
	if apiKeyHash != "" && !a.isTrustedProxyAPIKey(apiKey) {
		return OKEnvelope(ResponseInterceptResponse{})
	}
	if apiKeyHash == "" && apiKey != "" {
		apiKeyHash = hashAPIKey(apiKey)
	}
	if apiKeyHash == "" {
		return OKEnvelope(ResponseInterceptResponse{})
	}

	a.mu.RLock()
	binding, ok := a.state.KeyBindings[apiKeyHash]
	pool, poolOK := a.poolLocked(binding.PoolID)
	allowedModels := a.poolModelsLocked(pool)
	a.mu.RUnlock()

	if !ok {
		return OKEnvelope(ResponseInterceptResponse{})
	}
	if !poolOK || !pool.Enabled {
		filtered, changed := filterModelsResponse(req.Body, map[string]struct{}{})
		if !changed {
			return OKEnvelope(ResponseInterceptResponse{})
		}
		return OKEnvelope(ResponseInterceptResponse{Headers: jsonHeaders(), Body: filtered})
	}
	filtered, changed := filterModelsResponse(req.Body, allowedModels)
	if !changed {
		return OKEnvelope(ResponseInterceptResponse{})
	}
	return OKEnvelope(ResponseInterceptResponse{Headers: jsonHeaders(), Body: filtered})
}

func (a *App) poolModelsLocked(pool PoolConfig) map[string]struct{} {
	models := make(map[string]struct{})
	for _, model := range pool.Models {
		if normalized := normalizeModelID(model); normalized != "" {
			models[normalized] = struct{}{}
		}
	}
	if len(a.state.AuthModels) == 0 {
		return models
	}
	for _, authID := range poolCandidateAuthIDs(pool) {
		for _, model := range a.state.AuthModels[strings.TrimSpace(authID)] {
			if normalized := normalizeModelID(model); normalized != "" {
				models[normalized] = struct{}{}
			}
		}
	}
	return models
}

func isModelsEndpoint(req ResponseInterceptRequest) bool {
	for _, value := range []string{req.Path, req.RequestPath} {
		if isModelsPath(value) {
			return true
		}
	}
	if len(req.OriginalRequest) == 0 {
		return false
	}
	line := strings.SplitN(string(req.OriginalRequest), "\n", 2)[0]
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		return isModelsPath(parts[1])
	}
	return false
}

func isModelsPath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if parsed, err := url.Parse(value); err == nil {
		value = parsed.Path
	}
	value = strings.TrimRight(value, "/")
	return value == "/v1/models" || value == "/openai/v1/models" || strings.HasSuffix(value, "/v1/models")
}

func filterModelsResponse(body []byte, allowed map[string]struct{}) ([]byte, bool) {
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, false
	}
	filtered, changed := filterModelPayload(raw, allowed)
	if !changed {
		return nil, false
	}
	encoded, err := json.Marshal(filtered)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

func filterModelPayload(raw any, allowed map[string]struct{}) (any, bool) {
	switch typed := raw.(type) {
	case []any:
		return filterModelItems(typed, allowed)
	case map[string]any:
		for _, key := range []string{"data", "models", "items", "value"} {
			items, ok := typed[key].([]any)
			if !ok {
				continue
			}
			filtered, changed := filterModelItems(items, allowed)
			if !changed {
				return raw, false
			}
			clone := make(map[string]any, len(typed))
			for k, v := range typed {
				clone[k] = v
			}
			clone[key] = filtered
			return clone, true
		}
	}
	return raw, false
}

func filterModelItems(items []any, allowed map[string]struct{}) ([]any, bool) {
	filtered := make([]any, 0, len(items))
	for _, item := range items {
		modelID := normalizeModelID(modelIDFromItem(item))
		if modelID == "" {
			filtered = append(filtered, item)
			continue
		}
		if _, ok := allowed[modelID]; ok {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == len(items) {
		return items, false
	}
	return filtered, true
}

func normalizeModelID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func modelIDFromItem(item any) string {
	if text, ok := item.(string); ok {
		return strings.TrimSpace(text)
	}
	object, ok := item.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"id", "model", "name"} {
		if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func extractAPIKeyFromHTTPHeader(headers http.Header) string {
	flat := map[string][]string(headers)
	return extractAPIKey(flat)
}

func extractHelperAPIKeyHashFromHTTPHeader(headers http.Header) string {
	flat := map[string][]string(headers)
	return extractHelperAPIKeyHash(flat)
}

func jsonHeaders() http.Header {
	return http.Header{"Content-Type": []string{"application/json"}}
}

func cleanModelList(values []string) []string {
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
	sort.Strings(cleaned)
	return cleaned
}
