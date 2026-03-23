---
name: ussycode-web
description: Build and expose web applications in ussycode dev environments. Teaches correct binding, port usage, and public URL reporting. Use when building web apps, starting dev servers, or exposing services in an ussycode VM.
---

# ussycode Web Development

## How Web Apps Work in ussycode

Every ussycode VM gets automatic HTTPS via a public subdomain. When you run a web server inside the VM, it's accessible at:

```
https://<vm-name>.<public-domain>
```

## Critical Rules

1. **Bind to `0.0.0.0`**, never `localhost` or `127.0.0.1`
2. **Use port `8080`** — this is the default proxied port
3. **Report the public URL** to the user, not `localhost:8080`

## Framework Examples

### Python / http.server
```bash
python3 -m http.server 8080 --bind 0.0.0.0
```

### Vite / Next.js
```bash
npx vite --host 0.0.0.0 --port 8080
npx next dev -H 0.0.0.0 -p 8080
```

## Checking Public URL

Use the `ussycode_status` tool to get the current VM's public URL.

Use the `ussycode_publish` tool to verify a service is running and get its public URL.
