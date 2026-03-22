# Progress

## Status
Completed

## Tasks
- [x] Investigate share-link redemption gap
- [x] Fix `caddy.go`: wire `forward_auth` into every VM Caddy route
- [x] Fix `auth.go`: `extractVMName` — fall back to `X-Forwarded-Host` when called through Caddy
- [x] Fix `auth.go`: `extractShareToken` — parse `X-Forwarded-Uri` for the `?ussy_share=` param
- [x] Fix `auth.go`: `cleanRedirectURL` — reconstruct full URL from `X-Forwarded-*` headers
- [x] Fix `config.go`: add `AuthProxyURL` field (what Caddy connects to)
- [x] Fix `main.go`: pass `AuthProxyURL` into the proxy manager
- [x] Add tests for Caddy forward_auth header path + edge cases
- [x] `go build ./...` — clean
- [x] `go test ./internal/proxy/...` — 9/9 pass

## Files Changed
- `internal/proxy/auth.go` — split `extractVMName` into `extractVMName(r)+vmNameFromHost`; add `extractShareToken` (checks `X-Forwarded-Uri`); add `cleanRedirectURL` (reconstructs URL from `X-Forwarded-*`); import `net/url`
- `internal/proxy/caddy.go` — add `AuthProxyURL` to `Config`/`Manager`; add `URI`/`CopyHeaders` to `caddyHandler`; prepend `forward_auth` handler to every `AddRoute` subroute
- `internal/config/config.go` — add `AuthProxyURL` field (default `http://localhost:9876`), env `USSYCODE_AUTH_PROXY_URL`, CLI flag `-auth-proxy-url`
- `cmd/ussycode/main.go` — pass `AuthProxyURL: cfg.AuthProxyURL` to `proxy.NewManager`
- `internal/proxy/auth_test.go` — 5 new tests: `ForwardAuthVMNameFromXForwardedHost`, `ForwardAuthShareLinkRedemption`, `InvalidShareTokenForbidden`, `RevokedShareLinkCookieDenied`, `ShareTokenBelongsToWrongVM`

## Notes
Four distinct gaps were responsible for the end-to-end failure:

1. **Caddy routes had no `forward_auth`** (`caddy.go` `AddRoute`) — every request was
   proxied directly to the VM, completely bypassing the auth proxy.

2. **`extractVMName` used `r.Host` only** — through Caddy's `forward_auth`, `r.Host`
   is the auth proxy's own bind address (`localhost:9876`).  The real VM hostname
   arrives in `X-Forwarded-Host`.

3. **`?ussy_share=` was read from `r.URL.Query()`** — through `forward_auth`, Caddy
   sends the original request URI in `X-Forwarded-Uri`; `r.URL` points to the auth
   proxy endpoint, not the VM URL.

4. **The redirect URL was built by copying `r.URL`** — after redemption the browser
   would have been sent back to `http://localhost:9876/` instead of
   `https://demo.dev.ussyco.de/`.

All four fixes are backward-compatible: the direct (test) path continues to work
via `r.Host`/`r.URL`, and the `forward_auth` path is covered by the `X-Forwarded-*`
fallbacks.
