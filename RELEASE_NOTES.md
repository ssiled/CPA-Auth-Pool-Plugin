# CPA Auth Pool 0.1.23

This release adds bounded in-memory monitoring events for auth-pool scheduling and upstream completion, exposed to CPA-Helper-s for diagnosing pool selection failures.

## Monitoring

- Record scheduler decisions as `selected`, `blocked`, or `ignored`, including pool, model, provider, user, candidate counts and selected auth ID.
- Record upstream completion as `success` or `failed`, including the final auth ID, HTTP status and a bounded failure reason.
- Include up to 25 candidate account samples with provider, priority, status and detected account types.
- Keep the newest 500 events in an O(1) in-memory ring buffer; events are not written to the plugin state file.
- Add management endpoints:
  - `GET /v0/management/plugins/cpa-auth-pool/events`
  - `DELETE /v0/management/plugins/cpa-auth-pool/events`

## Security and retention

- API keys, Management Keys, Authorization headers and request bodies are not recorded.
- Failure reasons are limited to 320 characters.
- Events disappear when CPA restarts or the event buffer is cleared.

## Validation

- `go test ./...`

Add `https://raw.githubusercontent.com/ssiled/CPA-Auth-Pool-Plugin/main/registry.json` to `plugins.store-sources` to install or update the plugin.
