# Bifrost Plus (internal fork of maximhq/bifrost)

This is an internal, standalone fork of [maximhq/bifrost](https://github.com/maximhq/bifrost). It is not meant to be contributed back. The default branch `dev` holds upstream Bifrost plus:

- **OAuth subscription providers** `antigravity` (Google Antigravity / Cloud Code Assist) and `kiro` (AWS Kiro). Each key is one signed-in account. You can sign in from the dashboard (Google OAuth for Antigravity; a device code with AWS Builder ID, Google or GitHub for Kiro) or paste a refresh token or Kiro token JSON. Access tokens refresh automatically, and rotated credentials are written back to the key. See [Antigravity](docs/providers/supported-providers/antigravity.mdx) and [Kiro](docs/providers/supported-providers/kiro.mdx).
- **Per-provider key rotation** (`key_selection`): `weighted_random` (default), `round_robin` (weighted, optional `sticky_limit`), `least_used` and `fill_first`, plus cross-request cooldowns for keys that hit a rate limit, an exhausted quota or a rejected credential. Available for every provider. See [Key Rotation](docs/providers/key-rotation.mdx).

The provider ports are based on [OpenCodex](https://github.com/lidge-jun/opencodex) (MIT, see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)). Everything else is upstream Bifrost, and upstream's documentation applies.

---

## Build and run

Requirements: Go 1.27, a C compiler (CGO for SQLite), Node 22+ for the dashboard.

```bash
scripts/fork/build.sh              # go.work + dashboard + binary -> ./tmp/bifrost-http
scripts/fork/build.sh --skip-ui    # binary only (keeps the dashboard that is already built)
```

The modules keep their upstream `github.com/maximhq/bifrost/...` paths, and their `go.mod` files require the **published** upstream versions, which lack these features. Builds therefore go through a Go workspace (`go.work`, gitignored) that points every module at this checkout; `scripts/fork/workspace.sh` creates it. Do not use `make setup-workspace` or `go work sync`: they rewrite every `go.mod`/`go.sum`.

Minimal `./data/config.json`:

```json
{
  "setup_token": "change-me",
  "config_store": { "enabled": true, "type": "sqlite", "config": { "path": "./data/config.db" } },
  "governance": {
    "virtual_keys": [{
      "name": "local", "id": "vk-local", "value": "sk-bf-local", "is_active": true,
      "provider_configs": [
        { "provider": "antigravity", "allowed_models": ["*"], "key_ids": ["*"], "weight": 1 },
        { "provider": "kiro",        "allowed_models": ["*"], "key_ids": ["*"], "weight": 1 }
      ]
    }]
  }
}
```

```bash
./tmp/bifrost-http -app-dir ./data -port 8080
```

1. Open http://localhost:8080 and enter the setup token.
2. Go to **Model Providers → Add New Provider**, pick **Antigravity (Google OAuth)** or **Kiro (AWS OAuth)**, then **Add Key → Sign in**. Add one key per account.
3. Choose the rotation strategy in **Edit Provider Config → Key Rotation**.

```bash
curl http://localhost:8080/v1/chat/completions \
  -H 'x-bf-vk: sk-bf-local' -H 'Content-Type: application/json' \
  -d '{"model":"kiro/claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}'
```

### Docker

```bash
docker build -f transports/Dockerfile.local -t bifrost-plus .
docker run -p 8080:8080 -v "$PWD/data:/app/data" bifrost-plus
```

Use `transports/Dockerfile.local`, which builds from this checkout. The default `transports/Dockerfile` (and the public `npx`/`maximhq/bifrost` images) compile against the published upstream modules and do **not** contain this fork's features.

#### Releasing an image (GitHub Container Registry)

`.github/workflows/fork-docker.yml` builds `transports/Dockerfile.local` on native amd64 and arm64 runners, publishes `ghcr.io/buiducnhat/bifrost:v<version>` (plus `latest` for versions without a `-suffix`) as one multi-arch image, and smoke-tests `/health` on the published tag. It needs no secrets: it logs in with the built-in `GITHUB_TOKEN`.

```bash
git tag v0.1.0 && git push origin v0.1.0     # or: Actions -> Fork Docker Release -> Run workflow (version 0.1.0)
docker run -p 8080:8080 -v "$PWD/data:/app/data" ghcr.io/buiducnhat/bifrost:v0.1.0
```

The first push creates the package as private. To pull without logging in, set it to public once (GitHub profile -> Packages -> `bifrost` -> Package settings -> Change visibility); otherwise `docker login ghcr.io` with a token that has `read:packages`.

### Checking that everything still works

```bash
scripts/fork/verify.sh        # builds, vets and runs the fork's tests
scripts/fork/verify.sh --ui   # ...plus the dashboard build and its unit tests
```

It covers the rotation engine and core retry hooks, both providers, the `key_selection` plumbing, the fork's database migration, the sign-in endpoints and credential write-back. It does not run the rest of upstream's test suite, some of which also fails on upstream itself.

---

## Updating from upstream (optional)

You never have to. When you want upstream's newer features or fixes:

```bash
git checkout dev
scripts/fork/sync-upstream.sh          # merge upstream dev, then verify
scripts/fork/sync-upstream.sh --push   # ...and push dev
```

It merges `maximhq/bifrost` `dev` into this `dev`, turns on `git rerere` (a conflict you resolve once is resolved the same way next time), and regenerates `docs/openapi/openapi.json` when that is the only conflict left (needs `python3` with PyYAML). If it stops on conflicts, fix the listed files, then `git add -A && git commit --no-edit && scripts/fork/verify.sh`.

Most fork code lives in files upstream does not have (`core/providers/antigravity/`, `core/providers/kiro/`, `core/keyselectors/rotation.go`, `core/keyrotation.go`, `core/schemas/keyselection.go`, `core/schemas/keycredentials.go`, `framework/configstore/forkmigrations.go`, `transports/bifrost-http/handlers/oauth_subscriptions.go`, `transports/bifrost-http/lib/credential_updater.go`, the new UI fragments and API slice, the docs pages and `scripts/fork/`), so conflicts can only show up in these small hooks:

| File | Fork edit | How to resolve |
|------|-----------|----------------|
| `core/bifrost.go` | provider imports and factory `case`s, the `keyRotator` field and its `Init` line, `keyObserver` calls in `executeRequestWithRetries`, the `requestWorker` observer and `selectKeyWithStrategy` call, `SelectKeyForProviderRequestType` | Keep upstream's change and re-add the fork lines around it |
| `core/schemas/bifrost.go`, `core/schemas/provider.go`, `core/utils.go` | provider constants, `StandardProviders`, `ProviderConfig.KeySelection`, `validateKey` cases | Keep both |
| `framework/configstore/{clientconfig,rdb}.go`, `tables/provider.go`, `transports/bifrost-http/handlers/{providers,provider_keys}.go`, `lib/account.go` | `KeySelection` / `key_selection` next to every `PromptCache` / `prompt_cache`; credential validation | Keep both; add `KeySelection` to any new `PromptCache` site |
| `transports/bifrost-http/server/server.go` | sign-in handler creation and route registration (2 lines) | Keep both |
| `transports/config.schema.json`, `helm-charts/bifrost/values.schema.json`, `ui/...`, `docs/...yaml`, `docs/docs.json`, postman collection | provider entries, `key_selection`, sign-in UI, docs | Keep both |
| `docs/openapi/openapi.json` | generated | `cd docs/openapi && python3 bundle.py` after the YAML is resolved |

Keep new fork code in new files where you can, register new configstore migrations in `framework/configstore/forkmigrations.go`, and record fork changes in the changelog below rather than in the module `changelog.md` files. That keeps future updates cheap.

---

## Changelog

- **feat (core):** `antigravity` provider. Uses a Google Antigravity subscription through Cloud Code Assist, with the key value holding a Google OAuth refresh token, either bare or as JSON with `project_id`/`email`. Supports chat, chat streaming, Responses (served through chat) and list models (live `fetchAvailableModels` with a built-in fallback catalog). Refreshes tokens and discovers the project automatically; discovered projects and rotated refresh tokens are written back when the `Account` implements `schemas.KeyCredentialStore` (the HTTP gateway's does; Go SDK users can implement `UpdateKeyCredential` on their own account).
- **feat (core):** `kiro` provider. Uses a Kiro subscription through the Kiro runtime, with the key value holding a social refresh token or the Kiro IDE `kiro-auth-token.json` JSON (social or AWS SSO OIDC refresh). Supports chat, chat streaming, Responses (served through chat) and a static model list. Throttling, monthly quota and suspension are classified so rotation reacts to them, including errors sent inside the event stream.
- **feat (core):** provider-level key rotation `ProviderConfig.KeySelection` (`key_selection`): `weighted_random`, `round_robin`, `least_used` and `fill_first`, with `sticky_limit` and per-key cooldowns (per model for rate limits and quota, whole key for rejected credentials). An upstream `Retry-After` overrides `cooldown_seconds` (default 60; 0 disables). State is in memory, per process.
- **feat (framework):** the config store persists `key_selection` in `config_providers.key_selection_json` (migration `add_key_selection_json_column`).
- **feat (transports):** sign-in endpoints `POST /api/oauth-subscriptions/antigravity/{start,complete}` and `POST /api/oauth-subscriptions/kiro/{start,poll}`; refreshed credentials are written back to the key. Keys whose value is `env.VAR` are never overwritten.
- **feat (ui):** Antigravity and Kiro providers with sign-in helpers in the key form, and a **Key Rotation** tab in the provider settings.
- **chore:** build, verify and upstream-update scripts in `scripts/fork/`, and this guide.
- **fix (ui):** Antigravity and Kiro provider icons now use the official brand artwork (`ui/public/images/antigravity.png`, `kiro.svg`).
