# Handoff

## Completed: Track G — Deployment & Ops (all 4 subtasks)

## Next Task: None remaining — all tracks (A, E, G) and audit are complete

## Context:
- Track G created Ansible playbooks (6 roles), enhanced agent installer, created control plane installer, and wrote 5 docs
- `go build ./...` and `go test ./...` both pass clean
- LSP errors are known false positives — build is source of truth
- `PROGRESS-G.md` written at project root with full details
- `AUDIT-REPORT.md` from previous session also at project root
- The `internal/admin/embed.go` has a pre-existing `//go:embed` issue (empty web/templates dir) — NOT our bug

## Files Modified:
- `PROGRESS-G.md` — NEW (Track G progress report)
- `devplan.md` — Updated with Track G items marked complete
- `deploy/ansible/` — 24 files (site.yml, ansible.cfg, inventories, 6 roles with tasks/defaults/handlers/templates)
- `deploy/install-agent.sh` — Enhanced with checksum verification, distro detection
- `deploy/install-control.sh` — NEW (~500 lines, full control plane installer)
- `docs/getting-started.md` — NEW
- `docs/self-hosting.md` — NEW
- `docs/contributing-compute.md` — NEW
- `docs/architecture.md` — NEW
- `docs/api.md` — NEW
