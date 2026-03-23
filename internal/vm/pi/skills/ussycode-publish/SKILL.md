---
name: ussycode-publish
description: Publish and share web applications from ussycode VMs. Explains how public URLs work, how to verify service exposure, and how to share with others.
---

# Publishing from ussycode

## Public URL Format

Every VM gets a public HTTPS URL:
```
https://<vm-name>.<public-domain>
```

This URL proxies to port 8080 inside the VM with automatic TLS.

## Publishing Workflow

1. Start your app on `0.0.0.0:8080`
2. Run `/publish` or use the `ussycode_publish` tool to verify
3. Share the public URL with anyone

## Troubleshooting

If your app isn't accessible:
1. Check it's bound to `0.0.0.0`, not `localhost`
2. Check it's on port `8080`
3. Use `ss -tlnp | grep 8080` to verify
4. Use `curl localhost:8080` to test locally first
