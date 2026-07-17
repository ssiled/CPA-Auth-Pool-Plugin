package plugin

import (
	"encoding/json"
	"strings"
)

func (a *App) routeModel(raw []byte) ([]byte, error) {
	var req ModelRouteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	apiKey := extractAPIKeyFromHTTPHeader(req.Headers)
	apiKeyHash := extractHelperAPIKeyHash(map[string][]string(req.Headers))
	if apiKeyHash != "" && !a.isTrustedProxyAPIKey(apiKey) {
		return OKEnvelope(ModelRouteResponse{Handled: false})
	}
	if apiKeyHash == "" && apiKey != "" {
		apiKeyHash = hashAPIKey(apiKey)
	}
	if apiKeyHash == "" {
		return OKEnvelope(ModelRouteResponse{Handled: false})
	}

	a.mu.RLock()
	binding, ok := a.state.KeyBindings[apiKeyHash]
	pool, poolOK := a.poolLocked(binding.PoolID)
	allowedModels := a.poolModelsLocked(pool)
	a.mu.RUnlock()

	if !ok {
		return OKEnvelope(ModelRouteResponse{Handled: false})
	}
	if !poolOK || !pool.Enabled {
		return OKEnvelope(ModelRouteResponse{Handled: true, Reason: "cpa-auth-pool: pool unavailable"})
	}
	model := strings.TrimSpace(req.RequestedModel)
	if model == "" {
		model = modelFromBody(req.Body)
	}
	if model == "" {
		return OKEnvelope(ModelRouteResponse{Handled: false})
	}
	if _, ok := allowedModels[normalizeModelID(model)]; !ok {
		return OKEnvelope(ModelRouteResponse{Handled: true, Reason: "cpa-auth-pool: model outside bound pool"})
	}
	provider := providerForPool(pool, req.AvailableProviders)
	if provider == "" {
		return OKEnvelope(ModelRouteResponse{Handled: true, Reason: "cpa-auth-pool: provider unavailable for bound pool"})
	}
	return OKEnvelope(ModelRouteResponse{
		Handled:     true,
		TargetKind:  "provider",
		Target:      provider,
		TargetModel: model,
		Reason:      "cpa-auth-pool:" + pool.ID,
	})
}

func modelFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Model)
}

func providerForPool(pool PoolConfig, availableProviders []string) string {
	for _, provider := range providerPreferencesForPool(pool) {
		if resolved := resolveAvailableProvider(provider, availableProviders); resolved != "" {
			return resolved
		}
	}
	return ""
}

func providerPreferencesForPool(pool PoolConfig) []string {
	seen := map[string]bool{}
	providers := []string{}
	add := func(provider string) {
		provider = normalizeProviderKey(provider)
		if provider == "" || seen[provider] {
			return
		}
		seen[provider] = true
		providers = append(providers, provider)
	}
	for _, value := range pool.AccountTypes {
		for _, accountType := range accountTypeAliases(value) {
			switch accountType {
			case "codex", "free", "plus", "team", "pro", "enterprise", "supported", "k12", "edu", "education", "student":
				add("codex")
			case "gemini", "google":
				add("google")
				add("gemini")
			case "grok", "xai":
				add("xai")
			case "claude", "anthropic":
				add("anthropic")
			case "antigravity":
				add("antigravity")
			}
		}
	}
	for _, authID := range pool.AuthIDs {
		for _, accountType := range accountTypeAliases(authID) {
			switch accountType {
			case "codex":
				add("codex")
			case "gemini", "google":
				add("google")
				add("gemini")
			case "grok", "xai":
				add("xai")
			case "claude", "anthropic":
				add("anthropic")
			case "antigravity":
				add("antigravity")
			}
		}
	}
	return providers
}

func resolveAvailableProvider(provider string, availableProviders []string) string {
	provider = normalizeProviderKey(provider)
	if provider == "" {
		return ""
	}
	if len(availableProviders) == 0 {
		return provider
	}
	candidates := []string{provider}
	if provider == "xai" || provider == "grok" {
		candidates = append(candidates, "openai-compatible-xai", "openai-compatible-grok")
	}
	if provider == "google" || provider == "gemini" {
		candidates = append(candidates, "google", "gemini")
	}
	for _, candidate := range candidates {
		for _, available := range availableProviders {
			if strings.EqualFold(candidate, strings.TrimSpace(available)) {
				return strings.TrimSpace(available)
			}
		}
	}
	if provider == "xai" || provider == "grok" {
		for _, available := range availableProviders {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(available)), "openai-compatible-") && strings.Contains(strings.ToLower(available), "xai") {
				return strings.TrimSpace(available)
			}
		}
	}
	return ""
}

func normalizeProviderKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "google_api_key", "gemini_api_key", "gemini-api-key", "google-api-key":
		return "google"
	case "x_ai", "x.ai", "xai_api_key", "xai-api-key", "supergrok":
		return "xai"
	case "claude":
		return "anthropic"
	}
	return value
}
