# CPA Auth Pool 0.1.24

This release hardens trusted CPA-Helper proxy requests and serializes plugin state persistence.

## Security

- A request carrying a trusted `X-CPA-Helper-API-Key-Hash` header now fails closed when the hash has no plugin binding.
- Inconsistent Helper/Plugin state can no longer fall back to unrelated global CPA accounts.
- Direct CPA API keys without a Helper proxy header keep the existing unbound-key scheduling behavior.

## State safety

- Serialize state saves so concurrent management mutations cannot publish snapshots out of order.
- Retain the existing deep-copy, temporary-file, fsync and atomic-rename persistence path.

## Compatibility

- Use with CPA-Helper-s `v0.3.26` or newer.
- Use a CLIProxyAPI build containing the auth-pool priority-filter ordering fix.

## Validation

- `go test ./...`

Add `https://raw.githubusercontent.com/ssiled/CPA-Auth-Pool-Plugin/main/registry.json` to `plugins.store-sources` to install or update the plugin.
