# CPA Auth Pool Plugin

`cpa-auth-pool` is a CPA scheduler plugin shipped with CPA-Helper-s. It groups CPA auth accounts into pools and lets each CPA-Helper-s API key bind to one pool.

## What it does

- Manage CPA auth accounts as pools from CPA-Helper-s `Auth Pools`.
- Bind an API key to a request pool when creating or editing the key.
- At request time, the plugin hashes the incoming API key and schedules only auth candidates from the bound pool.
- Before CPA schedules a request, the plugin routes bound Codex/Gemini/Grok/Claude/Antigravity pools to their matching provider so CPA will not fall back to unrelated providers for that key.
- If a key is bound to a pool but that pool has no matching candidate, the plugin blocks fallback to other pools.
- Keep a bounded in-memory event log of scheduler decisions and upstream completion status for troubleshooting in CPA-Helper-s.
- API keys without pool bindings keep CPA default scheduling behavior.

## Build

Build on Linux or WSL:

```bash
cd CPA-Auth-Pool-Plugin
make build-linux
```

Artifacts are written to `dist/`:

- `cpa-auth-pool_linux_amd64.so`
- `cpa-auth-pool_linux_arm64.so`

## Install in CPA

Copy the `.so` file into CPA `plugins.dir`, then enable it in CPA config:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-auth-pool:
      enabled: true
      priority: 20
      state_file: "plugins/cpa-auth-pool-state.json"
```

Restart CPA. CPA-Helper-s uses these management endpoints:

```text
/v0/management/plugins/cpa-auth-pool/status
/v0/management/plugins/cpa-auth-pool/pools
/v0/management/plugins/cpa-auth-pool/bindings
/v0/management/plugins/cpa-auth-pool/events
```

## Use from CPA-Helper-s

1. Configure CPA URL and Management Key in settings.
2. Confirm CPA auth accounts are visible in the account inspection pages.
3. Open `Auth Pools`, create pools, and select accounts for each pool.
4. Open `API Keys`, create or edit a key, and choose a request pool.
5. Clients keep using the same CPA Base URL and API key; pool scheduling happens inside CPA.

## Notes

- Pool account IDs must match CPA scheduler candidate auth IDs. The UI currently uses account names from CPA-Helper-s account inspection.
- Provider routing depends on pool account types. Prefer type-based pools such as `free`, `plus`, `team`, `gemini`, or `grok` when you need strict provider isolation.
- Bound pools intentionally fail closed: empty or unavailable pools do not fall back to other pools.
- Back up `plugins/cpa-auth-pool-state.json` because it stores pool definitions and key bindings used by CPA runtime.
- Versions 0.1.18 and newer migrate a legacy `cpa-auth-pool-state.json` file into `plugins/cpa-auth-pool-state.json` when the new file is missing.
- Monitoring events are kept only in memory, capped at 500 entries, and disappear when CPA restarts or the buffer is cleared.


## Plugin Store URL

```text
https://raw.githubusercontent.com/ssiled/CPA-Auth-Pool-Plugin/main/registry.json
```


