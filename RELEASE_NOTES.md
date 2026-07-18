# CPA Auth Pool 0.1.28

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
