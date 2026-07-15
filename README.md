# CPA Auth Pool Plugin

`cpa-auth-pool` is a CPA scheduler plugin shipped with CPA-Helper-s. It groups CPA auth accounts into pools and lets each CPA-Helper-s API key bind to one pool.

## What it does

- Manage CPA auth accounts as pools from CPA-Helper-s `Auth Pools`.
- Bind an API key to a request pool when creating or editing the key.
- At request time, the plugin hashes the incoming API key and schedules only auth candidates from the bound pool.
- If a key is bound to a pool but that pool has no matching candidate, the plugin blocks fallback to other pools.
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
      state_file: "cpa-auth-pool-state.json"
```

Restart CPA. CPA-Helper-s uses these management endpoints:

```text
/v0/management/plugins/cpa-auth-pool/status
/v0/management/plugins/cpa-auth-pool/pools
/v0/management/plugins/cpa-auth-pool/bindings
```

## Use from CPA-Helper-s

1. Configure CPA URL and Management Key in settings.
2. Confirm CPA auth accounts are visible in the account inspection pages.
3. Open `Auth Pools`, create pools, and select accounts for each pool.
4. Open `API Keys`, create or edit a key, and choose a request pool.
5. Clients keep using the same CPA Base URL and API key; pool scheduling happens inside CPA.

## Notes

- Pool account IDs must match CPA scheduler candidate auth IDs. The UI currently uses account names from CPA-Helper-s account inspection.
- Bound pools intentionally fail closed: empty or unavailable pools do not fall back to other pools.
- Back up `cpa-auth-pool-state.json` because it stores pool definitions and key bindings used by CPA runtime.


## Plugin Store URL

`	ext
https://raw.githubusercontent.com/ssiled/-CPA-Auth-Pool-Plugin/main/registry.json
` 


