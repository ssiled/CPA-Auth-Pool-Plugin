package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
)

const (
	ABIVersion    = 1
	SchemaVersion = 1
	PluginID      = "cpa-auth-pool"
	PluginName    = "cpa-auth-pool"
	Version       = "0.1.29"

	MethodPluginRegister     = "plugin.register"
	MethodPluginReconfigure  = "plugin.reconfigure"
	MethodModelRoute         = "model.route"
	MethodSchedulerPick      = "scheduler.pick"
	MethodUsageHandle        = "usage.handle"
	MethodResponseIntercept  = "response.intercept_after"
	MethodManagementRegister = "management.register"
	MethodManagementHandle   = "management.handle"
)

type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *EnvelopeError  `json:"error,omitempty"`
}

type EnvelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type LifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type Registration struct {
	SchemaVersion uint32       `json:"schema_version"`
	Metadata      Metadata     `json:"metadata"`
	Capabilities  Capabilities `json:"capabilities"`
}

type Metadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository,omitempty"`
	ConfigFields     []ConfigField `json:"ConfigFields"`
}

type ConfigField struct {
	Name        string `json:"Name"`
	Type        string `json:"Type"`
	Description string `json:"Description"`
}

type Capabilities struct {
	ModelRouter         bool `json:"model_router,omitempty"`
	Scheduler           bool `json:"scheduler,omitempty"`
	ResponseInterceptor bool `json:"response_interceptor,omitempty"`
	ManagementAPI       bool `json:"management_api"`
	UsagePlugin         bool `json:"usage_plugin,omitempty"`
}

type SchedulerPickRequest struct {
	Provider   string                   `json:"Provider,omitempty"`
	Model      string                   `json:"Model"`
	Stream     bool                     `json:"Stream,omitempty"`
	Options    SchedulerPickOptions     `json:"Options"`
	Candidates []SchedulerAuthCandidate `json:"Candidates"`
}

type SchedulerPickOptions struct {
	Headers  map[string][]string `json:"Headers,omitempty"`
	Metadata map[string]any      `json:"Metadata,omitempty"`
}

type SchedulerAuthCandidate struct {
	ID         string            `json:"ID"`
	Provider   string            `json:"Provider"`
	Priority   int               `json:"Priority,omitempty"`
	Status     string            `json:"Status,omitempty"`
	Attributes map[string]string `json:"Attributes,omitempty"`
	Metadata   map[string]any    `json:"Metadata,omitempty"`
}

type SchedulerPickResponse struct {
	AuthID  string `json:"AuthID,omitempty"`
	Handled bool   `json:"Handled"`
}

type ModelRouteRequest struct {
	SourceFormat       string         `json:"SourceFormat,omitempty"`
	RequestedModel     string         `json:"RequestedModel"`
	Stream             bool           `json:"Stream,omitempty"`
	Headers            http.Header    `json:"Headers,omitempty"`
	Query              url.Values     `json:"Query,omitempty"`
	Body               []byte         `json:"Body,omitempty"`
	Metadata           map[string]any `json:"Metadata,omitempty"`
	AvailableProviders []string       `json:"AvailableProviders,omitempty"`
}

type ModelRouteResponse struct {
	Handled     bool   `json:"Handled"`
	TargetKind  string `json:"TargetKind,omitempty"`
	Target      string `json:"Target,omitempty"`
	TargetModel string `json:"TargetModel,omitempty"`
	Reason      string `json:"Reason,omitempty"`
}

type ResponseInterceptRequest struct {
	Method          string         `json:"Method,omitempty"`
	Path            string         `json:"Path,omitempty"`
	RequestPath     string         `json:"RequestPath,omitempty"`
	RequestedModel  string         `json:"RequestedModel,omitempty"`
	Stream          bool           `json:"Stream,omitempty"`
	RequestHeaders  http.Header    `json:"RequestHeaders,omitempty"`
	ResponseHeaders http.Header    `json:"ResponseHeaders,omitempty"`
	OriginalRequest []byte         `json:"OriginalRequest,omitempty"`
	RequestBody     []byte         `json:"RequestBody,omitempty"`
	Body            []byte         `json:"Body,omitempty"`
	StatusCode      int            `json:"StatusCode,omitempty"`
	Metadata        map[string]any `json:"Metadata,omitempty"`
}

type ResponseInterceptResponse struct {
	Headers      http.Header `json:"Headers,omitempty"`
	Body         []byte      `json:"Body,omitempty"`
	ClearHeaders []string    `json:"ClearHeaders,omitempty"`
}

type UsageRecord struct {
	Provider        string       `json:"Provider,omitempty"`
	Model           string       `json:"Model,omitempty"`
	AuthID          string       `json:"AuthID,omitempty"`
	AuthType        string       `json:"AuthType,omitempty"`
	Additional      bool         `json:"Additional,omitempty"`
	Failed          bool         `json:"Failed,omitempty"`
	Failure         UsageFailure `json:"Failure,omitempty"`
	ResponseHeaders http.Header  `json:"ResponseHeaders,omitempty"`
}

type UsageFailure struct {
	StatusCode int    `json:"StatusCode,omitempty"`
	Body       string `json:"Body,omitempty"`
}

type ManagementRegistrationRequest struct {
	BasePath string `json:"BasePath"`
}

type ManagementRegistrationResponse struct {
	Routes []ManagementRoute `json:"Routes"`
}

type ManagementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description,omitempty"`
}

type ManagementRequest struct {
	Method  string      `json:"Method"`
	Path    string      `json:"Path"`
	Headers http.Header `json:"Headers"`
	Query   url.Values  `json:"Query"`
	Body    []byte      `json:"Body"`
}

type ManagementResponse struct {
	StatusCode int         `json:"StatusCode,omitempty"`
	Headers    http.Header `json:"Headers,omitempty"`
	Body       []byte      `json:"Body,omitempty"`
}

func OKEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{OK: true, Result: raw})
}

func ErrorEnvelope(code, message string, status int) []byte {
	raw, _ := json.Marshal(Envelope{OK: false, Error: &EnvelopeError{Code: code, Message: message, HTTPStatus: status}})
	return raw
}
