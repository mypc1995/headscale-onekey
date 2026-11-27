# headscale-onekey

<p align="center">
  <img src="docs/badges/license.svg" alt="License: MIT">
  <img src="docs/badges/headscale.svg" alt="Headscale v0.29.7">
  <img src="docs/badges/go.svg" alt="Go 1.26">
  <img src="docs/badges/platform.svg" alt="Platform: Linux">
  <img src="docs/badges/arch.svg" alt="x86_64 and aarch64">
  <img src="docs/badges/shell.svg" alt="Shell: Bash">
  <img src="docs/badges/sqlite.svg" alt="Pure Go SQLite">
  <img src="docs/badges/dependencies.svg" alt="No external dependencies">
  <img src="docs/badges/i18n.svg" alt="i18n">
</p>

<p align="center">
  <b>A private Headscale control plane in one command — plus a web console that manages all of it.</b><br>
  <sub>Bundled admin UI · Zero external dependencies · 中文 / English · 14 themes</sub>
</p>

<p align="center">
  <a href="README.md">简体中文</a> · <b>English</b>
</p>

---

## What this is

`headscale-onekey` compresses "deploy a private [Headscale](https://github.com/juanfont/headscale) control server" into a single command: it downloads the official binary, creates the system user and directories, writes the systemd unit, configures the firewall, runs a config self-check (aborting on failure, then fixing data-directory ownership on success), derives a MagicDNS base domain that cannot collide with the `server_url` domain, and creates a default user plus a 24-hour pre-auth key.

The bundled **Headscale Admin** is a self-built full-featured admin UI shipped as a **single Go binary**. Copy it over and it runs: no Node, no Python, no database server, no external CDN. Nodes, users, keys, DNS, DERP, ACLs, certificates, monitoring, ports and prefixes can all be edited in the browser — with an automatic backup and a `configtest` gate before anything is applied.

```bash
git clone https://github.com/mypc1995/headscale-onekey.git
cd headscale-onekey

sudo bash deploy-headscale.sh -u http://192.168.1.10:18080   # 1. control server
sudo bash deploy-headscale-admin.sh --port 3001             # 2. admin UI
```

> Targets Ubuntu / Debian / CentOS / RHEL / Rocky / AlmaLinux / Fedora, on x86_64 and aarch64.
> Verified on WSL Ubuntu and AlmaLinux 9. The scripts currently follow the Headscale v0.29.7 configuration format.

## Features

| | Area and capability |
| :---: | --- |
| 🚀 | **One-command deploy** — official binary, system user, data directory, systemd unit, firewall rules and `configtest` self-check in one pass |
| 🔐 | **Three access modes** — `http://` plaintext for internal networks (port derived from the URL) · `https://` + `--tls-mode acme` (built-in ACME, automatic renewal) · `https://` + `--tls-mode certbot` (external certificates with a timer, restart after renewal) |
| 📦 | **Zero-dependency console** — single Go binary, inline icons, pure-Go SQLite driver: no CGO, no external CDN |
| 🧩 | **Full web configuration** — domain, ports, prefixes, DNS, DERP, ACL, TLS certificates and renewal are all graphical; backup + `configtest` before every save |
| 📈 | **Live monitoring** — host CPU/memory/load/uptime + per-node state, handshake, version, expiry and routes + 3-second bandwidth charts + 7 alert rules |
| 🌐 | **Per-node bandwidth** — a node-side agent reports cumulative counters and the server derives rates (the control plane has no such counter) |
| 🛡️ | **Security baseline** — PBKDF2 password digests, HttpOnly sessions, CSRF on every form, login throttling, session revocation on demotion, **a graphical captcha on public logins**, **security response headers and a strict CSP** |
| ⚡ | **A control plane that cannot wedge** — every external command has a timeout and a cancellable gate, so a stuck headscale call never freezes the whole UI |
| 🎨 | **i18n & themes** — one-click 中文/English switching, 14 color themes, stored in a cookie / browser local storage |

<details>
<summary><b>Table of contents</b></summary>

- [What this is](#what-this-is)
- [Features](#features)
- [Repository layout](#repository-layout)
- [Quick start](#quick-start)
  - [1. Deploy Headscale](#1-deploy-headscale)
  - [2. Deploy the admin UI](#2-deploy-the-admin-ui)
- [The admin UI](#the-admin-ui)
  - [Pages at a glance](#pages-at-a-glance)
  - [Settings](#settings)
  - [Network & policy](#network--policy)
  - [Ops & monitoring](#ops--monitoring)
- [Security design](#security-design)
  - [Login security](#login-security)
  - [Transport & response headers](#transport--response-headers)
  - [Shared egress and trusted sources](#shared-egress-and-trusted-sources)
- [Reliability design](#reliability-design)
- [Node speed reporting](#node-speed-reporting)
- [Verification](#verification)
- [Security notes](#security-notes)
- [License](#license)

</details>

## Repository layout

| File | Description |
| --- | --- |
| `deploy-headscale.sh` | One-click Headscale deployment |
| `deploy-headscale-admin.sh` | One-click deployment of the admin UI (port 3001 by default) |
| `headscale-admin/` | Go source, templates, and static assets of the admin UI |
| `node-speed-agent.sh` | Node-side collector that reports local Tailscale traffic counters to the admin UI |
| `test-headscale-cli.sh` | Read-only sandbox checks for the Headscale CLI and config keys |
| `test-headscale-admin.sh` | Admin UI checks: build, login, captcha, i18n, themes, bandwidth, config validation and CSRF |
| `test-headscale-admin-tls.sh` | End-to-end admin UI HTTPS checks on an isolated instance (never touches production) |

## Quick start

### 1. Deploy Headscale

**Internal HTTP**

```bash
sudo bash deploy-headscale.sh -u http://192.168.1.10:18080
```

**Public HTTPS with built-in ACME**

```bash
sudo bash deploy-headscale.sh -u https://hs.example.com -e admin@example.com --derp
```

**Public HTTPS with certbot**

```bash
sudo bash deploy-headscale.sh -u https://hs.example.com \
  --tls-mode certbot -e admin@example.com --derp
```

**Main options**

| Option | Description |
| --- | --- |
| `-u, --url URL` | Client-facing address; must start with `http://` or `https://` |
| `-e, --email MAIL` | ACME registration email; required in certbot mode |
| `--tls-mode MODE` | `acme` (HTTPS default), `certbot`, or `none` (HTTP default) |
| `--base-domain DOM` | MagicDNS base domain; derived automatically by default, rarely needs to be set |
| `--derp` | Enable the built-in DERP relay; recommended for public deployments |
| `-v, --version VER` | Pin a Headscale version; defaults to `0.29.7` |
| `--prefix-v4 CIDR` | Tailnet address range; defaults to the upstream `100.64.0.0/10`, e.g. `10.0.0.0/24` |
| `--public-ip IP` | Set the public IP explicitly; behind NAT auto-detection returns the private address, and the built-in DERP needs the public one |
| `--binary PATH` | Use a local headscale binary instead of downloading it from GitHub (offline deployment) |
| `--config PATH` | Use a local upstream config example instead of downloading it from GitHub (offline deployment) |

After deployment, manage certificates with `headscale-cert status|renew|reload`.

> **Note**: in v0.29.x, SIGHUP only reloads ACLs, not TLS certificates; use `renew` or restart the service to pick up new certificates.

**Offline deployment** (for servers that cannot reach GitHub — common on mainland networks): fetch the files on a machine that can, then upload them.

```bash
# on a machine with internet access
curl -fLO https://github.com/juanfont/headscale/releases/download/v0.29.7/headscale_0.29.7_linux_amd64
curl -fsSL https://raw.githubusercontent.com/juanfont/headscale/v0.29.7/config-example.yaml -o config-example.yaml
scp headscale_0.29.7_linux_amd64 config-example.yaml deploy-headscale.sh server:~/

# on the server (GitHub is never contacted)
sudo bash deploy-headscale.sh -u http://<public-IP>:18080 --tls-mode none \
  --binary ~/headscale_0.29.7_linux_amd64 --config ~/config-example.yaml \
  --prefix-v4 10.0.0.0/24 --public-ip <public-IP>
```

### 2. Deploy the admin UI

Listens on port 3001 by default with a built-in account system. The first deployment generates a random admin password and prints it once:

```bash
sudo bash deploy-headscale-admin.sh --port 3001
```

Or set the administrator explicitly:

```bash
sudo bash deploy-headscale-admin.sh --port 3001 \
  --admin-user admin --admin-pass 'YourStrongPassword'
```

| Scenario | How |
| --- | --- |
| Redeploy | Existing password is kept by default |
| Reset the password | Add `--reset-admin` |
| Restrict sources | Add `--allow-cidr 192.168.1.0/24` |
| Leave the firewall alone | Add `--no-firewall` |
| Low-spec server or no GitHub access | Build elsewhere and pass `--binary ./headscale-admin` |

Offline deployment (the admin UI is a single Go binary, so it only needs building once):

```bash
# on a machine with internet access (needs the repo's headscale-admin source tree)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o headscale-admin .
scp headscale-admin deploy-headscale-admin.sh server:~/
scp -r headscale-admin/templates headscale-admin/static server:~/

# on the server (no Go, no compilation)
sudo bash deploy-headscale-admin.sh --binary ~/headscale-admin
```

## The admin UI

The design uses a neo-brutalist style with warm backgrounds, thick borders, and hard shadows; all icons are inline with no external CDN dependencies. The globe icon in the header toggles Chinese/English (stored in a cookie; the login page can switch too), and the theme dropdown offers 14 palettes — light, dark, cyberpunk, ocean, sunset, forest, midnight, dracula, nord, rose, coffee, terminal, soft, synthwave (persisted in the browser).

### Pages at a glance

| Path | Page | Contents |
| --- | --- | --- |
| `/` | Overview | Users, nodes, online nodes, pending routes, keys, service status, certificate expiry |
| `/nodes` | Nodes | List, rename, expire, delete, tags, route approval (only routes that are not approved yet are queued, and a route leaves the queue once it is approved) |
| `/users` | Users | Create, rename, delete, with display name and email support |
| `/keys` | Keys & access | Generate/expire/delete pre-auth keys and API keys, plus a one-click client join command |
| `/network` | Network & policy | DNS / MagicDNS, DERP, ACL policy, full config editing |
| `/ops` | Ops & monitoring | Host resources, node state, live bandwidth, alerts, service status, logs, restart |
| `/admins` | Administrators | Add/remove/edit accounts, roles (admin / readonly), password changes |
| `/settings` | Settings | Domain, ports and prefixes, HTTPS certificates, automatic renewal, the admin UI's own HTTPS |

Details on **keys & access**:

- **Expiry dropdown**: 1 hour, 24 hours, 7 days, 30 days, 90 days, 1 year, or permanent (100 years, effectively never expires).
- **One-click join card**: `server_url` is read from the current configuration (takes effect once the IP/domain is bound and saved), and newly generated pre-auth keys are filled in automatically. The page assembles `tailscale up --login-server=<server_url> --authkey=<key>` in real time with one-click copy. When the address or key is missing, the command shows a placeholder and the copy button is disabled, so you never copy a half-finished command.
- **Masked by default**: click the eye icon to toggle visibility. Each key in the list can be copied individually or sent straight to the join card with "Fill into command". New keys are shown only once (held in a per-session one-time store and never returned again after refresh), and never appear in URLs or logs.
- **Type and notes**: the list has a Type column (single-use/reusable, used/unused, expired, ephemeral badges) and a Note column. Notes live in the admin UI's local SQLite (keyed by key ID) and are removed when the key is deleted.
- **API keys**: newly created keys appear in a prominent card with the full value and a copy button (once only, gone after refresh); the list only ever shows the prefix.
- headscale's `preauthkeys list` returns masked keys (`hskey-auth-xxxx-***`); masked entries offer no copy/fill buttons and are labeled "Full value shown only at creation".

### Settings

Path `/settings`. Common Headscale settings are consolidated into the web UI so you don't have to edit YAML by hand. **Each section is saved independently.** Fields use plain-language labels, with the original config key shown as a small tag beside each label for cross-referencing YAML and the official docs; the key names themselves are never changed.

**Domain and access address**

| Config key | Label | Validation |
| --- | --- | --- |
| `server_url` | Public access URL | Required; only `http://` / `https://`; no path, query, fragment, or trailing `/`; the scheme must match the certificate mode |
| `dns.base_domain` | Device domain suffix | Required; at least two labels; cannot be a bare IP; must differ from the `server_url` domain |
| `dns.magic_dns` | Enable MagicDNS | Toggle; devices can reach each other by name when enabled |

**Ports and prefixes**

| Config key | Label | Validation |
| --- | --- | --- |
| `listen_addr` | Client listen port | Required; `IP:port`, port 1–65535 |
| `grpc_listen_addr` | Local management port | Required; same format |
| `metrics_listen_addr` | Metrics port | Optional; same format |
| `prefixes.v4` | Device IPv4 prefix | Required; valid CIDR (not restricted to `100.64.0.0/10`) |
| `prefixes.v6` | Device IPv6 prefix | Required when "Enable IPv6" is checked; valid CIDR, prefix length no larger than /64 |

Duplicate ports within the same form are rejected outright. A warning appears when the `server_url` port differs from the listen port, preventing lockouts when changing ports.

The **IPv6 toggle** is off by default. When off, any existing `prefixes.v6` line is **removed from the config file** (keeping it would make headscale keep handing out IPv6 addresses); when on, the given prefix is written back. Prefixes are only validated as CIDR — final legality is delegated to `headscale configtest`, so the admin UI never rejects a valid config by being stricter than headscale.

> Some cloud providers (e.g. Alibaba Cloud ECS) occupy the 100.x range themselves, which conflicts with device virtual addresses; pick any valid range in that case (e.g. `10.99.0.0/24`).
> Changing prefixes only affects devices that join **afterwards** — existing node IPs stay the same.

**HTTPS certificates**

Three modes; switching modes only writes the keys the current mode needs:

| Mode | What it does |
| --- | --- |
| Automatic (Let's Encrypt) | Certificate domain (required FQDN), challenge type (HTTP-01 / TLS-ALPN-01), the temporary listen address used during validation, and the certificate cache directory |
| Upload an existing certificate | Upload three files: **leaf certificate**, **intermediate certificate(s)** and **private key**. The server normalizes the contents (strips BOM, converts CRLF to LF), orders the certificates into a complete chain by signature relationship (so a bundle delivered in the wrong order still works), then verifies the certificate/private-key pair and each link's signature with `tls.X509KeyPair`. Only after everything passes is anything written to `/etc/headscale/certs/manual.crt` and `manual.key` (mode 0600, owned by `headscale`), and the paths stored in `tls_cert_path` / `tls_key_path` |
| No HTTPS | Clears the ACME domain and certificate paths |

With uploaded certificates, leave all three fields empty to keep an already-configured certificate you are not changing; the uploaded private key is never echoed back to the page.

The top of the page parses the current certificate and shows its source, CN, issuer, validity window, days remaining, SANs, and whether the private key matches. A warning appears when expiry is within 30 days or the key does not match.

**Automatic certificate renewal**

- **Built-in ACME**: renewed automatically by the headscale process; no extra cron job needed.
- **certbot**: shows the `certbot.timer` enabled/running state, last/next run time, and certificate name; you can start/stop the timer, set the daily check time (written to `certbot.timer.d/override.conf` followed by `daemon-reload`), or renew immediately (runs `certbot renew` and restarts headscale).
- When certbot is not installed, an install hint is shown instead of unusable buttons.

**Admin UI HTTPS**

A separate card at the bottom of the settings page controls whether **the admin web UI you are using right now** is served over HTTPS. It is independent of the certificate settings above, which govern the headscale service:

- A toggle plus a certificate source: paste PEM (certificate and private key as text; verified as a pair, then written to `certs/admin.crt` and `admin.key` in the data directory, mode 0600), or reuse the certificate paths from headscale's manual mode.
- Once enabled, the admin UI **listens on HTTPS only** and the HTTP port stops responding; the session cookie's `Secure` flag is turned on automatically.
- If the certificate cannot be loaded, the failure is logged and the UI falls back to HTTP, so one bad configuration cannot lock you out of the ops console. **The session cookie's `Secure` flag is cleared together with that fallback** — browsers do not send a `Secure` cookie over plain HTTP, so a stale flag would kill the login right away and turn the safety valve into a lockout.
- Saving requires a restart of the admin UI to take effect (Go's `http.Server` cannot switch to TLS in place on the same port). Under systemd it restarts automatically; otherwise you are prompted to restart manually. Repeated saves cannot trip systemd's start rate limit (deployment disables it with `StartLimitIntervalSec=0`), so the service never ends up in a state it cannot restart out of.
- The configuration lives in `admin-tls.json` in the data directory (mode 0600). If the page becomes unreachable because of a certificate problem, delete that file and restart the service to return to HTTP.

**Save and apply flow**

1. The server validates every field; on failure the form is redisplayed with the offending fields highlighted, and nothing is written.
2. A candidate config is generated from the current YAML and written to `/etc/headscale/.config-candidate.yaml`.
3. `headscale configtest -c` validates it; on failure the candidate is deleted and the full error is returned, leaving the live config untouched.
4. The existing config is backed up to `config.yaml.bak.<timestamp>`, then atomically replaced.
5. With "restart immediately after save" checked, `systemctl restart headscale` runs and the service is verified active and listening; on failure the backup is restored and the service restarted again.
6. On success a banner shows the result and the backup filename.

When uploading a certificate, the certificate files land on disk before the config is written; if the subsequent `configtest` or restart fails, the certificate files are rolled back together with the config.

> Changing the listen port or `server_url` briefly interrupts client connections; switching between HTTP and HTTPS requires matching changes on both sides, and the page warns when they are inconsistent.

**Node speed reporting**

This block is read-only display and distribution: it shows the report endpoint, the report token and the channel state, each with a copy button. The token is only handed to the admin role; readonly accounts see "Administrators only". The operations page keeps just a channel-state badge and a link to this block.

The hostname in the endpoint comes from the Host header you used to reach the admin UI: an address-based Host is used as-is, and a hostname is only accepted when it appears in the headscale config as `server_url` or `dns.base_domain` — otherwise it falls back to `127.0.0.1:<port>`. That prevents a forged Host header from making an administrator copy an address pointing somewhere else. When nodes reach the server from inside the tailnet, replace the hostname with the server's tailnet IP.

### Network & policy

Path `/network`. Network settings can also be maintained as graphical forms here, **each block saved independently**:

| Block | Contents |
| --- | --- |
| **DNS** | `base_domain`, MagicDNS toggle, `override_local_dns` toggle, plus add/remove rows for DNS servers and search domains (each validated: an IP, or an `https://` / `tls://` / `quic://` URL) |
| **DERP** | Built-in relay toggle, `region_id` (default 900), `region_name` |
| **ACL** | A rule wizard takes source / destination / port and appends a generated `accept` rule to the editor; the body is still a JSON text editor |
| **Full config editing** | Rewrite the entire YAML directly, as a fallback entry point |

All four blocks share the same save path: validate → `headscale configtest` → backup → atomic write → optional restart.

### Ops & monitoring

Path `/ops`.

**Host resources** — reads `/proc/stat`, `/proc/meminfo`, `/proc/loadavg`, and `/proc/uptime` every 3 seconds, showing CPU usage, memory usage (used/total), system load (1/5/15 min), uptime, and 3-minute CPU/memory charts.

- CPU usage is derived from the cumulative tick delta between two samples and shows 0 on the first sample.
- Memory usage is `MemTotal - MemAvailable`, falling back to `MemFree + Buffers + Cached` on older kernels without `MemAvailable`.
- On non-Linux systems without `/proc`, the page shows an unavailable notice without affecting anything else.

**Node monitoring** — runs `headscale nodes list -o json` every 15 seconds in the background, caches the result, and shows total/online/offline/expired node counts plus each node's name, user, virtual IPs, online state, live speed (down/up), last handshake, client version, OS, key expiry, tags, and route counts. States are online, offline, or key-expired, with expiry taking precedence. When the CLI read fails, the last successful snapshot is kept and a banner notes that stale data is being shown. The table footer notes the update time and sampling interval.

**Node live speed** — traffic between nodes goes over a direct WireGuard path or DERP and never passes through the control server; neither headscale's CLI nor its Prometheus metrics carry a per-node traffic counter, so the speed source has to be collected on the node and reported back. The admin UI supports two channels, with **node reporting taking priority over the counter file**:

| Channel | When it fits | Configuration |
| --- | --- | --- |
| HTTP reporting (recommended) | The collector agent runs on the node | Set a report token to open `POST /api/speed` |
| Counter file | The collector agent runs next to the admin UI | Environment variable `HSA_NODE_SPEED_FILE` pointing at a JSON file |

With no channel connected, each node's speed column shows a "—" placeholder with the reason on hover; all other monitoring data is unaffected. When the counter fallback trips (node or agent restarted), that round's rate is treated as 0 rather than going negative. Rate units match the live bandwidth panel (KB/s base).

**Alerts** — 7 built-in rules: headscale service is not active, CPU over threshold, memory over threshold, 1-minute load above cores×factor, node offline, node key expired, node data collection failed.

| Variable | Default | Meaning |
| --- | --- | --- |
| `HSA_ALERT_CPU` | 85 | CPU usage percentage threshold |
| `HSA_ALERT_MEM` | 90 | Memory usage percentage threshold |
| `HSA_ALERT_LOAD` | 2 | 1-minute load ceiling = CPU cores × this factor |

**Interface** — `GET /api/monitor` requires a login and returns `host`, `nodes`, `alerts` and `thresholds`; the page polls every 5 seconds and reuses the server's 15-second node cache, so opening several tabs does not hammer the CLI.

<details>
<summary><b>Extending: adding a monitoring metric or an alert rule</b></summary>

- **New host metric**: add a field to `HostSnapshot` in `hostmon.go` and fill it in `sample()` (e.g. read `/proc/mounts` to measure disk usage), extend `newHostView` with the display text, and add a bar to the frontend.
- **New node metric**: read the field from the CLI JSON in `nodeStatFromMap` (`nodemont.go`) and add it to `NodeStat`, then add a column to the template and to `nodeRow` in `static/app.js`; for counter-style metrics that need differencing (such as speed), add a data source the way `nodeSpeedRates` does.
- **New alert rule**: implement `func(alertInput, AlertThresholds) []MonitorAlert` in `alerts.go` and register it in the `alertRules` slice; neither the page nor the interface output needs to change. Add the threshold to `AlertThresholds` and read it from an environment variable in `defaultAlertThresholds`.
- **Wiring a notification channel**: `MonitorAlert`'s `Key` and `Level` are stable, so rules can be de-duplicated by key after evaluation and pushed to email or a webhook.

</details>

## Security design

### Login security

- Passwords are stored as PBKDF2-HMAC-SHA256 digests (120,000 iterations) with a per-account salt.
- Sessions use an HttpOnly cookie, every form carries a CSRF token, and the readonly role cannot submit modifications.
- **Logins from public sources show a graphical captcha**: a 4-digit numeric PNG with noise, interference lines and per-digit jitter. **It is checked before the password** — a wrong captcha means PBKDF2 never runs, so an attacker cannot burn your CPU. Tokens live in server memory (TTL 5 minutes, **single use**, capped at 1024 entries) and are handed out through an HttpOnly cookie named `hsa_cap`; the image is never cached, so a refresh yields a new one.
- **There is exactly one exemption rule, fixed in code with no switch**: the source is listed in `HSA_LOGIN_TRUSTED_IPS`, or the source is a loopback address **and** the Host is also a loopback/local name. Both halves of the second condition are required — a public request forwarded by a same-host reverse proxy still carries a public Host and therefore **still needs the captcha**, otherwise the captcha becomes decorative the moment you put a proxy in front.
- Login failures are counted along two axes, "source IP + account name" and "source IP": after repeated wrong guesses the source is progressively slowed for that account, and too many failures from one IP trigger a temporary lock (30 attempts by default, 2-minute lock). A non-existent username costs the same full key-derivation effort, so it cannot be used to enumerate accounts.
- A successful login clears only the "source IP + account name" counter and never the per-IP total — otherwise an attacker with one legitimate account could launder their throttle state away.
- The login request body is capped at 64 KB and returns 413 beyond that; password verification runs behind a concurrency gate (CPU cores minus 2, at most 8 in parallel) and returns 429 when the queue times out, so dozens of parallel logins cannot saturate a machine that is also running the headscale control plane.
- When an administrator demotes, disables, deletes or re-passwords an account, that account's existing sessions **stop working immediately**; `--reset-admin` revokes them as well.

### Transport & response headers

The admin UI is exposed to the internet, and the cost of each missing header is concrete: without `X-Frame-Options` the UI can be clickjacked (someone embeds it in their own page and induces an administrator to click "Save"); without a CSP a single XSS can walk away with the session cookie and the CSRF token; without `nosniff` a browser may parse the response as something else; without HSTS the very first plain-HTTP visit can be downgraded on the way.

| Header | Value | Why |
| --- | --- | --- |
| `X-Frame-Options` | `DENY` | Blocks clickjacking |
| `X-Content-Type-Options` | `nosniff` | The browser does not guess content types |
| `Referrer-Policy` | `same-origin` | The admin URL is not leaked to other sites |
| `Content-Security-Policy` | `default-src 'self'`; `script-src 'self'`; `style-src 'self' 'unsafe-inline'`; `img-src 'self' data:`; `font-src 'self'`; `connect-src 'self'`; `object-src 'none'`; `base-uri 'none'`; `form-action 'self'`; `frame-ancestors 'none'` | Minimizes the blast radius of an XSS |
| `Strict-Transport-Security` | `max-age=31536000` | **Added only when TLS is terminated directly** (behind a TLS-terminating proxy the server cannot see `r.TLS`, so the proxy must add it) |

`script-src` deliberately does **not** include `'unsafe-inline'`: no inline scripts are allowed on any page, so an XSS gets no second chance (the theme bootstrap script was moved into its own `static/theme.js` for this reason). `style-src` does allow `'unsafe-inline'` because progress-bar widths and theme swatches use inline style attributes, which are not scripts.

**Static assets carry a content-hash version plus `Cache-Control: no-cache`**: Go's `http.FileServer` sends only `Last-Modified` and no `Cache-Control`, so browsers apply heuristic caching ("10% of the file's age") and can keep serving a stale stylesheet for hours after a new release. The two measures are complementary and neither is optional: the versioned URL fixes copies that already sit in a cache, and `no-cache` means you never have to remember to bump a version by hand.

### Shared egress and trusted sources

Login throttling counts per source IP. Behind NAT, a corporate egress, or a reverse proxy, everybody shares one address, and a single mis-click would lock out the whole team. Two options:

```bash
# Treat the ops subnet as trusted: those sources neither accumulate failures,
# get locked, or need a captcha
# Comma-separated; single IPs and CIDRs are both accepted
Environment="HSA_LOGIN_TRUSTED_IPS=192.168.1.0/24,10.8.0.5"
```

Note: **the admin UI does not parse `X-Forwarded-For`** — it trusts the TCP source address. Behind a reverse proxy every request looks like it comes from the proxy itself, so never add the proxy's address (especially `127.0.0.1`) to the trusted list; that would turn throttling off for the entire internet.

Administrator accounts and sessions live in SQLite (`/var/lib/headscale-admin/headscale-admin.db`, mode 0600), so logins survive a service restart. On first start, if an old `admins.json` is found it is migrated automatically and the original is renamed to `admins.json.migrated`; existing passwords keep working. The SQLite driver is a pure-Go implementation (`modernc.org/sqlite`), so the build needs neither gcc nor CGO.

The source tree lives in `headscale-admin/` and depends on no third-party project with an undeclared license.

## Reliability design

The admin UI drives headscale through its command line. The failure mode that bites hardest here is "one headscale call hangs and the whole interface freezes", so:

- **Every external command has a timeout**, and on timeout the whole process group is killed (a `sh -c` may contain openssl / find grandchildren; killing only the direct child leaves orphans holding file locks).
- **All calls queue behind a cancellable gate**: when the gate cannot be taken the request returns a clear "headscale is busy" instead of waiting forever. Waiting for the gate and running the command share one timeout budget, so a call never starts only to be killed immediately.
- **stdout and stderr are kept apart**: headscale's JSON goes to stdout and its hints to stderr; merging them would put hints inside the JSON and break parsing.
- **Actions that change system state do not inherit the request context** (`systemctl restart` uses its own context) — otherwise a dropped HTTP request would cancel the restart and leave the service half-way between old and new.
- **The overview page no longer spawns processes on every refresh**: the version is cached for 10 minutes, certificate expiry for 5 minutes, and list results for 5 seconds; service state is reused from the node-monitoring snapshot. The cache serves the **read-only overview page only** — the node, user and key pages still read fresh data, so a rename or delete shows up immediately. **Only successful results are cached**, so a single transient failure is never cached and pinned.
- The timeout tiers can be shortened with `HSA_HS_CALL_TIMEOUT` (seconds), which is handy in test environments.

## Node speed reporting

**HTTP reporting channel** — the deployment script generates a report token and writes it to `/var/lib/headscale-admin/node-speed-token` (mode 0600, reused across upgrades); the token can also be supplied directly via `HSA_NODE_SPEED_TOKEN`.

> **With no token configured the whole endpoint is disabled and returns 404**, so it never becomes an anonymous write path anyone can use.

```bash
# Single report: node may be the node ID, the node name, or a tailnet IP — all three match
curl -X POST http://<server-tailnet-address>:3001/api/speed \
  -H "X-Node-Speed-Token: <report-token>" \
  -H 'Content-Type: application/json' \
  -d '{"node":"100.64.0.5","rx_bytes":154600,"tx_bytes":31220}'

# Arrays are accepted too; the token may also go in the body "token" field or an Authorization: Bearer header
curl -X POST http://<server-tailnet-address>:3001/api/speed \
  -H "Authorization: Bearer <report-token>" \
  -d '[{"node":"100.64.0.5","rx_bytes":154600,"tx_bytes":31220}]'
```

`rx_bytes` / `tx_bytes` are **cumulative** byte counters; the server derives the rate from the delta between two reports (the same idea as the tick delta behind CPU usage) and picks B/s, KB/s or MB/s automatically. A node's speed column starts showing after two consecutive reports.

**Node-side collector** — `node-speed-agent.sh` is a ready-to-use reference implementation: it finds the `tailscale*` interface by itself, reads the cumulative counters from `/proc/net/dev` and reports on a timer:

```bash
HSA_URL=http://<server-tailnet-address>:3001/api/speed \
HSA_TOKEN=<report-token> \
bash node-speed-agent.sh
```

| Optional variable | Description |
| --- | --- |
| `HSA_IFACE` | Pick the interface explicitly |
| `HSA_INTERVAL` | Report interval in seconds, default 10, recommended ≤ 15 |
| `HSA_NODE` | Node identifier; defaults to the local tailnet IP |
| `HSA_ONCE=1` | Report once and exit, for a self-check |

**Counter file channel** — environment variable `HSA_NODE_SPEED_FILE` points at a JSON file that a side-car collector on the same host keeps updated with cumulative RX/TX bytes; the server derives rates from the delta between two samples. The file is keyed by node ID or node name:

```json
{
  "12": {"rx_bytes": 154600, "tx_bytes": 31220},
  "laptop": {"rx_bytes": 1000, "tx_bytes": 500}
}
```

> A node can only collect control-plane metrics. The Tailscale client does not report host CPU, memory and the like to headscale, so per-node CPU/memory is out of scope for this page; if you need it, deploy the agent on the node and push the data to a new endpoint as described above.

## Verification

Read-only checks run on any Linux box (including WSL). They **never create users, write to `/etc` or start services**:

```bash
bash test-headscale-cli.sh      # CLI commands, config keys and configtest
bash test-headscale-admin.sh    # Admin UI: build, login, captcha, i18n, themes, bandwidth,
                                # host and node monitoring, config validation, IPv6 toggle
                                # (including on-disk config checks), network forms, CSRF,
                                # login throttling, readonly accounts and revocation (needs go/curl)
(cd headscale-admin && go test ./...)
                                # Unit tests: config parsing, admin UI HTTPS certificates,
                                # node mapping, i18n rendering, alert rules, account roles,
                                # TTL caches and security response headers
```

End-to-end verification of the admin UI's HTTPS needs a Linux machine that can write to `/etc/systemd/system` (it starts its own systemd unit). It **never touches production services**:

```bash
bash test-headscale-admin-tls.sh /usr/local/bin/headscale-admin
                                # An isolated instance (own port + own unit) runs enable →
                                # certificate failure fallback → certificate recovery →
                                # disable → reuse the headscale manual certificate,
                                # asserting the cookie's Secure flag and login usability
                                # at every step
```

## Security notes

| Item | Notes |
| --- | --- |
| Transport encryption | The admin UI has its own account system; passwords are stored as PBKDF2 digests and the session is an HttpOnly cookie. Over HTTP the transport is still plaintext, so use HTTPS or `--allow-cidr` to restrict sources on a public deployment |
| Passwords never in argv | The initial and reset admin passwords can be passed via environment variables (`HSA_INIT_ADMIN`, `HSA_RESET_ADMIN`, format `username:password`). Command-line arguments show up in `/proc/<pid>/cmdline` where any local user can read them; the deployment script uses environment variables by default |
| Firewall | The deployment script opens TCP/3001 to every source. For internal use, add `--allow-cidr`; for public exposure, enable the admin UI's HTTPS first |
| Two independent HTTPS setups | The admin UI's HTTPS and the headscale service's HTTPS are separate configurations that can be enabled independently. Once the admin UI's HTTPS is on, that port only accepts HTTPS and `http://` requests are not redirected; if a certificate problem makes the page unreachable, delete `admin-tls.json` from the data directory and restart the service to return to HTTP |
| Plaintext warning | In HTTP mode the traffic is plaintext; newer Tailscale clients may need `TS_ALLOW_HTTP_CONTROL_SERVER=1` |
| Domain and ports | HTTPS mode needs the domain to resolve to the host and ports 80 (certificate validation) and 443 (service) to be reachable; the built-in DERP additionally needs UDP/3478 |

## License

The scripts, documentation and admin UI source in this repository are released under the [MIT License](LICENSE).
