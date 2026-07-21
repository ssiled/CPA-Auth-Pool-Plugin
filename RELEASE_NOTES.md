# CPA Auth Pool 0.1.31

This release adds pool-wide concurrency protection and targeted failure failover.

## Scheduling protection

- Add `max_concurrency` per auth pool, with `0` retaining unlimited backward-compatible behavior.
- Check and reserve pool-wide and per-account slots atomically, then release the exact pool slot from completion attribution.
- Keep a bounded TTL fallback when the host completion cannot be uniquely linked to a pending selection.

## Targeted cooldowns

- Cache `model_not_supported` by normalized auth ID and model for 30 minutes without blocking other models on that account.
- Add 15-second account cooldowns for connection refusal/reset, timeout, DNS, TLS, EOF and other network failures.
- Persist both cooldown types and clear the matching model cache after a successful completion.

## Validation

- `go test ./...`
- `go vet ./...`
- GitHub Actions Linux amd64/arm64 CGO plugin builds

## Previous 0.1.30

This release adds conservative request ownership attribution for CPA-Helper-s usage records.

## Usage attribution

- Include API key hashes and descriptions in authenticated scheduler event diagnostics.
- Correlate completion events with pending selections only when the auth, model and provider identify one owner.
- Leave concurrent cross-user completions unattributed when the plugin ABI does not provide a request identifier.
- Keep pending correlation state bounded and expire it after 30 minutes.

## Validation

- `go test ./...`
- `go vet ./...`
- GitHub Actions Linux amd64/arm64 CGO plugin builds

## Previous 0.1.29

This release adds configurable per-pool scheduling.

## Scheduling strategies

- Keep `round-robin` as the backward-compatible default for existing pools.
- Add `fill-first` to keep using the highest-priority stable account until its per-account concurrency capacity is full.
- Move to the next account only when the preferred account is full, unavailable, or excluded during retry.
- Persist and validate `scheduling_strategy` through the pool management API.
- Expose the configured strategy in plugin status for CPA-Helper-s.

## Failure diagnostics and failover

- Classify model-support, SOCKS proxy, usage-limit, rate-limit and generic upstream failures in plugin events.
- Include sanitized raw error details, plan type and quota reset timing for CPA-Helper-s monitoring.
- Put accounts returning HTTP 429 into a persisted cooldown and select another pool member until quota recovery.
- Prefer upstream `resets_at`, then `resets_in_seconds`, with bounded fallback cooldowns when neither is present.

## Previous 0.1.28 changes

This release adds provider-channel pools without requiring CLIProxyAPI changes.

## Provider channels

- Match scheduler candidates against the pool's explicit provider identifiers.
- Include every credential currently configured under the selected OpenAI-compatible channel.
- Keep provider channels dynamic as credentials are added or removed in CPA.
- Preserve least-loaded selection and round-robin tie breaking across channel credentials.

## Previous 0.1.27 changes

This release hardens per-account Codex concurrency scheduling for concurrent multi-user traffic.

## Concurrency

- Select and reserve an account atomically under one lock.
- Prefer the least-loaded account within the current priority group.
- Use round-robin order to break equal-load ties.
- Keep limits scoped to each account, never to the aggregate tier count.
- Report plugin version, concurrency scope, strategy, and per-account live slots through the status API.
- Return `auth_pool_busy` only after every eligible account is at its own limit.
- Ignore auxiliary-model usage records when releasing the primary request slot.

## Scheduling

- Filter candidates by bound auth pool before applying logical priority.
- Resolve priority as account override, then account type rule, then host fallback.
- Preserve stable round-robin scheduling for accounts with the same logical priority.
- Exclude negative host-priority and unavailable candidates.

## Management

- Add the authenticated `/auth-priorities` management route.
- Persist account types, type priorities and per-account overrides.
- Support full replacement and removal of stale account overrides.
- Validate logical priorities in the `0..100` range.

## Compatibility and diagnostics

- Normalize auth file names, email-derived identifiers and `root_cli_proxy_api_*` identifiers.
- Report input, pool-matched and eligible candidate counts without logging credentials.
- Keep host priority as an availability signal when used with a compatible CPA-Helper-s build.

## Validation

- `go test ./...`
- `go vet ./...`
- GitHub Actions Linux amd64 CGO plugin build

Add `https://raw.githubusercontent.com/ssiled/CPA-Auth-Pool-Plugin/main/registry.json` to `plugins.store-sources` to install or update the plugin.
