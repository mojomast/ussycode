# Getting Started with ussycode

ussycode gives you instant, disposable dev environments accessible via SSH. No signup forms, no credit cards -- just connect with your SSH key.

## Quick Start

```bash
ssh ussyco.de
```

That's it. On first connect, ussycode registers your SSH key and gives you a handle (username). You'll land in an interactive shell where you can manage your environments.

ussycode is part of the **Ussyverse** — a community of AI-powered dev environments. Run `community` to see links, stats, and connect with others.

## Creating Your First VM

Once connected to the ussycode shell:

```
> new
```

This creates a new dev environment with the default Ubuntu-based image (`ussyuntu`). New VMs are sized for interactive work and OpenCode by default, typically `2 vCPU / 2048 MB` unless your trust limits cap them lower. You can customize it:

```
> new --name=myproject --image=ussyuntu
```

### Available Options

| Flag | Description | Default |
|------|-------------|---------|
| `--name=<name>` | Name your environment | auto-generated |
| `--image=<image>` | Base image to use | `ussyuntu` |

> **New here?** Run `tutorial` for a hands-on 10-lesson walkthrough covering VM lifecycle, web servers, sharing, and more.

## Listing Your Environments

```
> ls
```

For detailed output:

```
> ls -l
```

## Connecting to Your Environment

SSH into a running environment:

```
> ssh myproject
```

This drops you into a full Linux shell inside your microVM. You have root access, can install packages, run servers, and do anything you'd do on a real machine.

### Direct SSH Access

You can also connect directly from your terminal without going through the ussycode shell:

```bash
ssh -p 2222 myproject@ussyco.de
```

## Accessing via HTTPS

Every environment gets a subdomain automatically:

```
https://myproject-yourhandle.ussyco.de
```

If you're running a web server inside your VM, ussycode proxies port `8080` automatically and handles TLS for you.

Use:

```bash
python3 -m http.server 8080 --bind 0.0.0.0
```

Important rules:

- bind to `0.0.0.0`, not `localhost`
- prefer port `8080`
- then open `https://myproject-yourhandle.ussyco.de`

If you start a server on `localhost:8000`, it will only be visible inside the VM.

## Managing Environments

### Stop and Start

```
> stop myproject    # pause the VM (preserves disk)
> start myproject   # resume the VM
> restart myproject # stop + start
```

### Clone and Rename

```
> cp myproject backup       # clone an environment
> rename backup production  # rename it
```

### Tags

```
> tag myproject golang     # add a tag
> tag -d myproject golang  # remove a tag
```

### Delete

```
> rm myproject
```

## Sharing Access

Share your environment with others:

```
> share url myproject          # generate a shareable HTTPS URL
> share collab myproject user  # grant SSH access to another user
> share pub myproject          # make publicly accessible
```

### Custom Domains

Point your own domain to an environment:

```
> share cname myproject dev.example.com
```

Then add a CNAME record pointing `dev.example.com` to `ussyco.de` and a TXT record at `_ussycode-verify.dev.example.com` for verification:

```
> share cname-verify dev.example.com
```

## Using Templates

List available templates:

```
> new --image=<tab>
```

The default `ussyuntu` image includes:
- Ubuntu 24.04 LTS base
- Common dev tools (git, curl, build-essential)
- SSH server configured
- pi coding agent (default AI assistant, auto-launches on SSH)
- OpenCode (optional alternate AI client)
- AI-aware skills for web development and publishing

## AI Assistance In Your VM

### pi (Default)

When you SSH into a VM, **pi** launches automatically as your AI coding assistant. pi is pre-configured with:

- **ussyrouter** as the default LLM provider (your budget is managed automatically)
- **ussyverse theme** for a branded terminal experience
- **ussycode-aware skills** that teach the AI how web apps, URLs, and publishing work
- **Built-in tools** for checking status, budget, and publishing your apps

On your first login, pi will show a brief welcome screen and help you get oriented.

**Useful commands inside pi:**
- `/publish` — check if your app is accessible and get the public URL
- `/usage` — check your remaining budget
- Type naturally to ask the AI for help building, debugging, or deploying

**Exiting pi:**
- Press `Ctrl+C` or type `/exit` to return to a normal shell
- Run `pi` to restart it anytime

### OpenCode (Optional)

OpenCode is also installed as an alternate AI client. It does not launch automatically.

```bash
opencode    # run manually if preferred
```

OpenCode is pre-configured with the same ussyrouter proxy and budget as pi.

## SSH Key Management

```
> ssh-key list          # list your registered keys
> ssh-key add <key>     # add another SSH key
> ssh-key rm <id>       # remove a key
```

## Machine-Readable Output

Most commands support `--json` for scripting:

```
> ls --json
```

## Next Steps

- Run `help` to see all available commands
- Run `tutorial` for an interactive 10-lesson walkthrough
- Run `community` to learn about the ussyverse
