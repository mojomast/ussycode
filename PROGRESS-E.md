# Track E: API & Admin — Progress

## E.1: HTTPS API ✅

### Files Created
- `internal/api/handler.go` -- Main API handler with POST /exec, GET /health, GET /version
- `internal/api/ratelimit.go` -- Token bucket rate limiter per SSH key fingerprint
- `internal/api/handler_test.go` -- 18 tests covering all endpoints and error cases
- `internal/db/migrations/004_api_tokens.sql` -- Migration for api_tokens table

### Files Modified
- `internal/db/models.go` -- Added APIToken model
- `internal/db/queries.go` -- Added CRUD methods for API tokens

### Features Implemented
- **POST /exec** endpoint with JSON and text/plain body support
- **GET /health** -- returns status + timestamp
- **GET /version** -- returns version + Go version
- **Authentication**:
  - `usy0.` stateless tokens (SSH-key-signed permissions JSON)
  - `usy1.` short tokens (DB-backed, opaque token ID)
- **Token permissions**: time-based (exp/nbf) + command whitelist
- **Rate limiting**: per-fingerprint token bucket (60 req/min default, burst 10)
- **Error codes**: 400, 401, 403, 404, 405, 413, 422, 429, 500, 504
- **API token CRUD**: Create, Read, List, Revoke, Touch (last_used_at), Delete

### Test Results
All 18 tests passing.

## E.2: Admin Web Panel ✅

### Files Created
- `internal/admin/admin.go` -- Main admin handler (760+ lines): sessions, auth, routes, DB queries, template helpers
- `internal/admin/embed.go` -- go:embed directive for WebFS (templates + static assets)
- `internal/admin/admin_test.go` -- 27 tests covering session mgmt, auth, login flow, all page handlers, trust level updates, template helpers
- `internal/admin/web/static/style.css` -- Dark theme CSS with purple accent, terminal aesthetic
- `internal/admin/web/templates/layout.html` -- Shared layout with sidebar nav, logout form
- `internal/admin/web/templates/login.html` -- Login page with magic link instructions
- `internal/admin/web/templates/dashboard.html` -- Dashboard with stats grid + recent users table
- `internal/admin/web/templates/users.html` -- Users table with VM/key counts
- `internal/admin/web/templates/user_detail.html` -- User detail with trust level form, SSH keys table, VMs table
- `internal/admin/web/templates/vms.html` -- VMs table with owner, status, resources, IP
- `internal/admin/web/templates/vm_detail.html` -- VM detail grid with tags, shares table
- `internal/admin/web/templates/nodes.html` -- Nodes placeholder page (scheduler.NodeProvider not in SQLite)

### Features Implemented
- **Authentication**: Magic link token login via `/admin/login/callback?token=xxx`
- **Session management**: In-memory session store with auto-cleanup goroutine, 24h TTL, secure cookies
- **Trust level gate**: Only `operator` and `admin` users can access the panel
- **Dashboard**: Aggregate stats (users, VMs by status, shares, API tokens) + recent users
- **Users page**: Full user listing with VM/key counts (correlated subqueries)
- **User detail**: User info, SSH keys, VMs, trust level update form (POST)
- **VMs page**: All VMs with owner handles (LEFT JOIN), status badges
- **VM detail**: Full VM info, tags, shares
- **Nodes page**: Placeholder for scheduler.NodeProvider integration
- **CSS handler**: Serves embedded static CSS with cache headers
- **Template helpers**: `timeAgo`, `trustBadge`, `statusBadge`, `nullStr`, `nullInt`, `truncate`, `add`

### Routes
- `GET /admin/` -- Dashboard (auth required)
- `GET /admin/login` -- Login page
- `GET /admin/login/callback` -- Magic link callback
- `POST /admin/logout` -- Logout
- `GET /admin/users` -- Users list (auth required)
- `GET /admin/users/{id}` -- User detail (auth required)
- `POST /admin/users/{id}/trust` -- Update trust level (auth required)
- `GET /admin/vms` -- VMs list (auth required)
- `GET /admin/vms/{id}` -- VM detail (auth required)
- `GET /admin/nodes` -- Nodes (auth required, placeholder)
- `GET /admin/static/style.css` -- Static CSS

### Test Results
All 27 tests passing:
- Session store: Create/Get, expired session, delete, clean expired, unique IDs
- Auth middleware: no cookie redirect, invalid session redirect, valid session access
- Login page: renders OK
- Login callback: missing token, invalid token, non-admin rejected, admin success, operator success
- Logout: session deleted, cookie cleared
- Dashboard: authenticated with stats
- Users page: lists users
- User detail: shows user/VM/key data, invalid ID (400), not found (404)
- Trust level: update success, invalid level rejected
- VMs page: lists VMs with owners
- VM detail: shows VM data, not found (404)
- Nodes page: shows placeholder
- CSS handler: serves CSS with correct content type
- Template helpers: timeAgo (9 cases), trustBadge (5 cases), statusBadge (5 cases)

## E.3: Trust Levels & Quotas ✅

### Files Created
- `internal/db/migrations/008_user_quotas.sql` -- Adds `vm_limit`, `cpu_limit`, `ram_limit_mb`, `disk_limit_mb` columns to users table
- `internal/db/quota_test.go` -- 7 tests covering trust levels, quotas, and enforcement

### Files Modified
- `internal/db/models.go` -- Added `TrustLimits` struct, `ValidTrustLevels`, `IsValidTrustLevel()`, `GetTrustLimits()`
- `internal/db/queries.go` -- Added `GetUserTrustLevel`, `SetUserTrustLevel`, `GetUserVMCount`, `GetUserQuotas`
- `internal/ssh/commands.go` -- Added quota checks in `cmdNew`/`cmdCp`, `admin` command with `set-trust` subcommand, conditional admin help section

### Features Implemented
- **Trust levels**: newbie (3 VMs, 1 CPU, 2GB RAM, 5GB disk), citizen (10/4/8GB/25GB), operator (25/8/16GB/100GB), admin (unlimited)
- **Quota enforcement**: VM creation (`new`, `cp`) checks user's VM count against trust level limits
- **Admin command**: `admin set-trust <handle> <level>` -- admin-only gate on trust_level
- **DB queries**: Trust level CRUD, quota retrieval, VM count (alias for API consistency)

### Test Results
All 7 quota tests passing:
- `TestGetTrustLimits` (4 levels + unknown fallback)
- `TestIsValidTrustLevel` (4 valid + 5 invalid)
- `TestGetUserTrustLevel` (default newbie)
- `TestSetUserTrustLevel` (upgrade to citizen, admin; verify quotas updated; invalid level error)
- `TestGetUserVMCount` (0 count, 3 after creates)
- `TestQuotaEnforcement` (limit reached, upgrade expands, admin unlimited, delete decreases)
- `TestGetUserQuotas` (newbie defaults match)

## E.4: Custom Domains ✅

### Files Created
- `internal/db/migrations/009_custom_domains.sql` -- Creates `custom_domains` table with vm_id, domain (UNIQUE), verified, verification_token, created_at, verified_at; CASCADE delete on vm removal
- `internal/db/custom_domain_test.go` -- 8 tests covering full CRUD, duplicate prevention, cascade delete, and constraint enforcement

### Files Modified
- `internal/db/models.go` -- Added `CustomDomain` struct
- `internal/db/queries.go` -- Added `CreateCustomDomain`, `GetCustomDomain`, `VerifyCustomDomain`, `ListCustomDomains`, `DeleteCustomDomain`
- `internal/ssh/commands.go` -- Added `share cname`, `share cname-verify`, `share cname-rm` subcommands; updated share help text; added `isValidDomain` helper
- `internal/proxy/caddy.go` -- Added `AddCustomDomain(domain, vmName)` and `RemoveCustomDomain(domain)` methods with `customDomainRouteID` helper

### Features Implemented
- **Custom domain mapping**: `share cname <vm> <domain>` creates a domain record with a verification token
- **DNS verification**: `share cname-verify <vm> <domain>` performs a `net.LookupTXT` on `_ussycode-verify.<domain>` to verify ownership
- **Domain removal**: `share cname-rm <vm> <domain>` removes the domain and its proxy route
- **Proxy integration**: Verified domains get a Caddy reverse proxy route via `AddCustomDomain`; routes are cleaned up on removal
- **Domain validation**: `isValidDomain()` checks length (3-253), requires at least one dot, validates label format (a-z, 0-9, hyphens, max 63 chars)
- **Duplicate prevention**: UNIQUE constraint on domain column prevents same domain on multiple VMs
- **Cascade delete**: Deleting a VM automatically removes its custom domains (ON DELETE CASCADE)
- **User guidance**: Commands display DNS setup instructions (CNAME record + TXT verification record)

### SSH Commands
- `share cname <vm> <domain>` -- Add custom domain, display verification instructions
- `share cname-verify <vm> <domain>` -- Verify DNS TXT record and activate domain
- `share cname-rm <vm> <domain>` -- Remove custom domain and proxy route

### Test Results
All 8 custom domain tests passing:
- `TestCreateCustomDomain` -- Creates domain, verifies fields
- `TestCreateCustomDomain_Duplicate` -- UNIQUE constraint enforcement
- `TestVerifyCustomDomain` -- Marks domain verified, sets verified_at
- `TestListCustomDomains` -- Lists 3 domains for VM, 0 for other VM
- `TestDeleteCustomDomain` -- Deletes domain, confirms gone
- `TestGetCustomDomain_NotFound` -- Returns sql.ErrNoRows
- `TestCustomDomain_CascadeDelete` -- VM deletion cascades to custom domains
- `TestIsValidDomainLogic` -- UNIQUE constraint across different VMs
