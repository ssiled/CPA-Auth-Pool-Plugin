# CPA Auth Pool 0.1.25

This release adds pool-scoped logical priorities for CPA-Helper-s dual-layer scheduling.

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
