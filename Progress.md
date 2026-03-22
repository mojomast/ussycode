# Progress

## Status
Completed

## Tasks
- [x] Check existing tunnel routes (`tunnel config` + `tunnel all-routes`)
- [x] Add tunnel ingress routes (api, root, admin)
- [x] Add DNS CNAME records (api.ussycode, ussycode, admin.ussycode, *.ussycode)
- [x] Verify DNS records

## Files Changed
_No local files changed — all changes are in Cloudflare (tunnel config + DNS)_

## Notes
- Tunnel: LTC-HOME-CFTUNNEL (67375d79-d01c-4d27-8521-f7eede905f1b) — was already 🟢 healthy
- CLI `add-route` worked fine against the token-managed tunnel — no raw API call needed
- Catch-all `* → http_status:404` remains last in ingress order (correct)
- All 4 DNS records are proxied (orange cloud) via Cloudflare

### Tunnel ingress added
| Hostname | Backend | Port |
|---|---|---|
| api.ussycode.shuv.dev | 192.168.122.206 | 3000 (routussy proxy) |
| ussycode.shuv.dev | 192.168.122.206 | 8080 (HTTP API) |
| admin.ussycode.shuv.dev | 192.168.122.206 | 9090 (admin panel) |

### DNS CNAMEs added (shuv.dev)
| Name | Content | Proxied |
|---|---|---|
| api.ussycode | 67375d79-…cfargotunnel.com | ✅ |
| ussycode | 67375d79-…cfargotunnel.com | ✅ |
| admin.ussycode | 67375d79-…cfargotunnel.com | ✅ |
| *.ussycode | 67375d79-…cfargotunnel.com | ✅ |

### Next steps (not part of this task)
- Ensure ussycode VM (192.168.122.206) is running and services are listening on :3000, :8080, :9090
- cloudflared on shuvdev needs network access to 192.168.122.206 (libvirt NAT — should be fine from host)
- Test end-to-end: `curl https://api.ussycode.shuv.dev/health` etc.
