# headscale-onekey

[简体中文](README.md) | **English**

One-click deployment of a private [Headscale](https://github.com/juanfont/headscale) control server, bundled with a self-built full-featured admin UI, Headscale Admin (a single Go binary with Chinese/English UI, multiple color themes, live bandwidth charts, and web-based configuration management).

Targets Ubuntu / Debian / CentOS / RHEL / Rocky / AlmaLinux / Fedora, on x86_64 and aarch64. Verified on WSL Ubuntu and AlmaLinux 9. The scripts currently follow the Headscale v0.29.7 configuration format.

## Features

- Downloads the official binary, creates the system user and directories, generates a systemd unit, and configures the firewall.
- Three access modes:
  - `http://`: plain HTTP for internal networks; the listen port is derived from the URL.
  - `https://` + `--tls-mode acme`: Headscale's built-in ACME, with automatic certificate issuance and renewal.
  - `https://` + `--tls-mode certbot`: external certificates with a systemd timer for renewal, restarting the service after each renewal.
- Automatically derives a MagicDNS `base_domain` to avoid conflicts with the `server_url` domain.
- Built-in `configtest` self-check that aborts on failure; fixes data directory ownership on success.
- Creates a default user and a 24-hour pre-auth key automatically.
- Deploys the Headscale Admin UI: one-click Chinese/English switching, 14 color themes, live NIC traffic charts, a full account and permission system, and web-based domain/IP/SSL certificate management.

## Repository layout

| File | Description |
| --- | --- |
| `deploy-headscale.sh` | One-click Headscale deployment |
| `deploy-headscale-admin.sh` | One-click deployment of the full admin UI (port 3001 by default) |
| `headscale-admin/` | Go source, templates, and static assets of the admin UI |
| `remove-headcontrol.sh` | Cleans up HeadControl (port 3000) when upgrading from older versions |
| `test-headscale-cli.sh` | Read-only sandbox checks for Headscale CLI and config keys |
| `test-headscale-admin.sh` | Admin UI checks: build, login, i18n, themes, bandwidth, config validation, and CSRF |
| `node-speed-agent.sh` | Node-side speed collector that reports local Tailscale traffic counters to the admin UI |

## Quick start

Clone the repository and enter the directory:

```bash
git clone https://github.com/mypc1995/headscale-onekey.git
cd headscale-onekey
```

### 1. Deploy Headscale

Internal HTTP mode:

```bash
sudo bash deploy-headscale.sh -u http://192.168.1.10:18080
```

Public HTTPS mode with built-in ACME:

```bash
sudo bash deploy-headscale.sh -u https://hs.example.com -e admin@example.com --derp
```

Public HTTPS mode with certbot:

```bash
sudo bash deploy-headscale.sh -u https://hs.example.com \
  --tls-mode certbot -e admin@example.com --derp
```

Main options:

| Option | Description |
| --- | --- |
| `-u, --url URL` | Client-facing address; must start with `http://` or `https://` |
| `-e, --email MAIL` | ACME registration email; required in certbot mode |
| `--tls-mode MODE` | `acme` (HTTPS default), `certbot`, or `none` (HTTP default) |
| `--base-domain DOM` | MagicDNS base domain; derived automatically by default, rarely needs to be set |
| `--derp` | Enable the built-in DERP relay; recommended for public deployments |
| `-v, --version VER` | Pin a Headscale version; defaults to `0.29.7` |

After deployment, manage certificates with `headscale-cert status|renew|reload`. Note: in v0.29.x, SIGHUP only reloads ACLs, not TLS certificates; use `renew` or restart the service to pick up new certificates.

### 2. Deploy the admin UI (Headscale Admin)

A single Go binary with a Chinese/English UI, listening on port 3001 by default, with a built-in account system. No Node or Python required.

```bash
sudo bash deploy-headscale-admin.sh --port 3001
```

The first deployment generates a random admin password and prints it once; you can also set it explicitly:

```bash
sudo bash deploy-headscale-admin.sh --port 3001 \
  --admin-user admin --admin-pass 'YourStrongPassword'
```

Redeploying keeps the existing password; add `--reset-admin` to reset it. Use `--allow-cidr` to restrict client sources and `--no-firewall` to leave firewall rules untouched.

Feature coverage:

- Overview: users, nodes, online nodes, routes, keys, service status, certificate expiry.
- Nodes: list, rename, expire, delete, tags, route approval.
- Users: create, rename, delete, with display name and email support.
- Keys & access: generate/expire/delete pre-auth keys and API keys, plus a one-click client join command. Expiry is a dropdown: 1 hour, 24 hours, 7 days, 30 days, 90 days, 1 year, or permanent (100 years, effectively never expires).
  - One-click join card: `server_url` is read from the current configuration (takes effect once the IP/domain is bound and saved), and newly generated pre-auth keys are filled in automatically. The page assembles `tailscale up --login-server=<server_url> --authkey=<key>` in real time with one-click copy. When the address or key is missing, the command shows a placeholder and the copy button is disabled, so you never copy a half-finished command.
  - Keys are masked by default; click the eye icon to toggle visibility. Each key in the list can be copied individually or sent straight to the join card with "Fill into command". New keys are shown only once (held in a per-session one-time store and never returned again after refresh), and never appear in URLs or logs.
  - The pre-auth key list has a Type column (single-use/reusable, used/unused, expired, ephemeral badges) and a Note column: notes can be set at creation and edited later in the list. Notes live in the admin UI's local SQLite (keyed by key ID) and are removed when the key is deleted.
  - Newly created API keys are shown in a prominent card with the full value and a copy button (once only, gone after refresh) instead of a fleeting banner; the list only ever shows the prefix.
  - headscale's `preauthkeys list` returns masked keys (`hskey-auth-xxxx-***`); masked entries offer no copy/fill buttons and are labeled "Full value shown only at creation".
- Network & policy: DNS/MagicDNS, DERP, ACL policy, and full config editing as separate graphical forms — see "Network & policy page"; every save is backed up and validated with `configtest` first.
- Ops & monitoring: host CPU/memory/load/uptime (3-second sampling, 3-minute charts), node online state with handshake/version/expiry/routes (15-second sampling), threshold alerts, service status, version, certificate expiry, recent logs, service restart, and live bandwidth refreshed every 3 seconds (reads `/proc/net/dev`, with up/down rates, cumulative traffic, per-NIC details, and recent history; degrades to an unavailable notice on non-Linux systems).
- Administrators: add/remove/edit admin accounts, roles (admin/readonly), password changes.
- Configuration management: domain, ports and prefixes (IPv4 required, IPv6 optional via toggle), HTTPS certificates, automatic renewal, and the admin UI's own HTTPS are all managed on the web — see the next section.

#### Configuration page (`/settings`)

Common Headscale settings are consolidated into the web UI so you don't have to edit YAML by hand. The page has four independent sections, each saved separately. Fields use plain-language labels, with the original config key shown as a small tag beside each label for cross-referencing YAML and the official docs; the key names themselves are never changed.

**Domain and access address**

| Label | Config key | Validation |
| --- | --- | --- |
| Public access URL | `server_url` | Required; only `http://` / `https://`; no path, query, fragment, or trailing `/`; the scheme must match the certificate mode |
| Device domain suffix | `dns.base_domain` | Required; at least two labels; cannot be a bare IP; must differ from the `server_url` domain |
| Enable MagicDNS | `dns.magic_dns` | Toggle; devices can reach each other by name when enabled |

**Ports and prefixes**

| Label | Config key | Validation |
| --- | --- | --- |
| Client listen port | `listen_addr` | Required; `IP:port`, port 1–65535 |
| Local management port | `grpc_listen_addr` | Required; same format |
| Metrics port | `metrics_listen_addr` | Optional; same format |
| Device IPv4 prefix | `prefixes.v4` | Required; valid CIDR (not restricted to `100.64.0.0/10`) |
| Device IPv6 prefix | `prefixes.v6` | Required when "Enable IPv6" is checked; valid CIDR, prefix length no larger than /64 |

Duplicate ports within the same form are rejected outright. A warning appears when the `server_url` port differs from the listen port, preventing lockouts when changing ports.

IPv6 is disabled by default; when the toggle is off, `prefixes.v6` is not written (rather than keeping the previous value). Prefixes are only validated as CIDR — final legality is delegated to `headscale configtest`, so the admin UI never rejects a valid config by being stricter than headscale. Some cloud providers (e.g. Alibaba Cloud ECS) occupy the 100.x range themselves, which conflicts with device virtual addresses; pick any valid range in that case (e.g. `10.99.0.0/24`). Note that changing prefixes only affects devices that join **afterwards** — existing node IPs stay the same.

**Node speed reporting**

This block is read-only display and distribution: it shows the report endpoint, the report token and the channel state, each with a copy button. The token is only handed to the admin role; readonly accounts see "Administrators only". The operations page keeps just a channel-state badge and a link to this block.

The hostname in the endpoint comes from the Host header you used to reach the admin UI: an address-based Host is used as-is, and a hostname is only accepted when it appears in the headscale config as `server_url` or `dns.base_domain` — otherwise it falls back to `127.0.0.1:<port>`. That prevents a forged Host header from making an administrator copy an address pointing somewhere else. When nodes reach the server from inside the tailnet, replace the hostname with the server's tailnet IP.

#### Network & policy page (`/network`)

Network settings can also be maintained as graphical forms on `/network`, each block saved independently:

- **DNS**: `base_domain`, MagicDNS toggle, `override_local_dns` toggle, plus add/remove rows for DNS servers and search domains. Each DNS server is validated (IP, or `https://` / `tls://` / `quic://` URL) and each search domain is validated.
- **DERP**: built-in relay toggle, `region_id` (default 900), `region_name`.
- **ACL**: a rule wizard takes source / destination / port and appends a generated `accept` rule to the editor; the body is still a JSON text editor.
- **Full config editing**: rewrite the entire YAML directly, as a fallback entry point.

All four blocks share the same save path: validate → `headscale configtest` → backup → atomic write → optional restart.

**HTTPS certificates**

Three modes; switching modes only writes the keys the current mode needs:

- **Automatic (Let's Encrypt)**: certificate domain (required FQDN), expiry notification email (optional, must be valid if set), challenge type (HTTP-01 / TLS-ALPN-01), the temporary listen address used during validation, and the certificate cache directory.
- **Paste an existing certificate**: both the certificate and private key are pasted as PEM text — no file upload and no server paths to fill in. On save, both blocks are parsed and verified as a pair with `tls.X509KeyPair`, then written to `/etc/headscale/certs/manual.crt` and `manual.key` (mode 0600, owned by `headscale`), and those two paths are stored in `tls_cert_path` / `tls_key_path`. If a certificate is already configured and you are not changing it, leave both fields empty to keep it; pasted content is never echoed back to the page.
- **No HTTPS**: clears the ACME domain and certificate paths.

The top of the page parses the current certificate and shows its source (automatic / manual), CN, issuer, validity window, days remaining, SANs, and whether the private key matches. A warning appears when expiry is within 30 days or the key does not match.

**Automatic certificate renewal**

- Built-in ACME: shown as renewed automatically by the headscale process; no extra cron job needed.
- certbot: shows the `certbot.timer` enabled/running state, last/next run time, and certificate name; you can start/stop the timer, set the daily check time (written to `certbot.timer.d/override.conf` followed by `daemon-reload`), or renew immediately (runs `certbot renew` and restarts headscale).
- When certbot is not installed, an install hint is shown instead of unusable buttons.

**Admin UI HTTPS**

A separate "Admin UI HTTPS" card at the bottom of the settings page controls whether **the admin web UI you are using right now** is served over HTTPS. It is independent of the certificate settings above, which govern the headscale service:

- A toggle plus a certificate source: paste PEM (certificate and private key as text; verified as a pair, then written to `certs/admin.crt` and `admin.key` in the data directory, mode 0600), or reuse the certificate paths from headscale's manual mode.
- Once enabled, the admin UI **listens on HTTPS only** and the HTTP port stops responding; the session cookie's `Secure` flag is turned on automatically.
- If the certificate cannot be loaded, the failure is logged and the UI falls back to HTTP, so one bad configuration cannot lock you out of the ops console.
- Saving requires a restart of the admin UI to take effect (Go's `http.Server` cannot switch to TLS in place on the same port). Under systemd it restarts automatically; otherwise you are prompted to restart manually.
- The configuration lives in `admin-tls.json` in the data directory (mode 0600). If the page becomes unreachable because of a certificate problem, delete that file and restart the service to return to HTTP.

**Save and apply flow**

1. The server validates every field; on failure the form is redisplayed with the offending fields highlighted, and nothing is written.
2. A candidate config is generated from the current YAML and written to `/etc/headscale/.config-candidate.yaml`.
3. `headscale configtest -c` validates it; on failure the candidate is deleted and the full error is returned, leaving the live config untouched.
4. The existing config is backed up to `config.yaml.bak.<timestamp>`, then atomically replaced.
5. With "restart immediately after save" checked, `systemctl restart headscale` runs and the service is verified active and listening; on failure the backup is restored and the service restarted again.
6. On success a banner shows the result and the backup filename.

When pasting a certificate, the certificate files land on disk before the config is written; if the subsequent `configtest` or restart fails, the certificate files are rolled back together with the config.

Changing the listen port or `server_url` briefly interrupts client connections; switching between HTTP and HTTPS requires matching changes on both sides, and the page warns when they are inconsistent.

The UI supports Chinese/English switching: the globe icon in the header toggles the language, the choice is stored in a cookie, and the login page can be switched too. The theme dropdown offers 14 palettes (light, dark, cyberpunk, ocean, sunset, forest, midnight, dracula, nord, rose, coffee, terminal, soft, synthwave), persisted in the browser. The design uses a neo-brutalist style with warm backgrounds, thick borders, and hard shadows; all icons are inline with no external CDN dependencies.

Authentication uses an independent account system: passwords are stored with salted PBKDF2-HMAC-SHA256 (120,000 iterations), sessions use HttpOnly cookies, every form carries a CSRF token, and the readonly role cannot submit changes. Failed logins are counted per account and per source IP: repeated failures make that account's logins progressively slower, and too many failures from one IP lock it out temporarily. Usernames that do not exist still pay the full key-derivation cost, so they cannot be enumerated by timing. When an administrator demotes, disables, deletes an account or changes its password, that account's live sessions are revoked immediately — revocation takes effect at once.

Admin accounts and sessions live in SQLite (`/var/lib/headscale-admin/headscale-admin.db`, mode 0600), so logins survive service restarts. On first startup, a legacy `admins.json` is migrated automatically and renamed to `admins.json.migrated`; existing passwords keep working. The SQLite driver is pure Go (`modernc.org/sqlite`), so no gcc or CGO is needed to build.

The source lives in `headscale-admin/` and depends on no third-party project with an undeclared license.

#### Ops & monitoring page (`/ops`)

**Host resources**: reads `/proc/stat`, `/proc/meminfo`, `/proc/loadavg`, and `/proc/uptime` every 3 seconds, showing CPU usage, memory usage (used/total), system load (1/5/15 min), uptime, and 3-minute CPU/memory charts. CPU usage is derived from the cumulative tick delta between two samples and shows 0 on the first sample; memory usage is `MemTotal - MemAvailable`, falling back to `MemFree + Buffers + Cached` on older kernels without `MemAvailable`. On non-Linux systems without `/proc`, the page shows an unavailable notice without affecting anything else.

**Node monitoring**: runs `headscale nodes list -o json` every 15 seconds in the background, caches the result, and shows total/online/offline/expired node counts plus each node's name, user, virtual IPs, online state, live speed (down/up), last handshake, client version, OS, key expiry, tags, and route counts. States are online, offline, or key-expired, with expiry taking precedence. When the CLI read fails, the last successful snapshot is kept and a banner notes that stale data is being shown. The table footer notes the update time and sampling interval.

**Per-node live speed**: node-to-node traffic flows over WireGuard direct connections or DERP, never through the control server, and neither the headscale CLI nor its Prometheus metrics expose per-node traffic counters. The bandwidth data source therefore has to be collected on the nodes and sent back. Two channels are supported, and **node reporting takes priority over the counter file**:

| Channel | Use case | Configuration |
| --- | --- | --- |
| HTTP reporting (recommended) | The collector runs on the node | Configure a report token to enable `POST /api/speed` |
| Counter file | The collector runs next to the admin UI | Environment variable `HSA_NODE_SPEED_FILE` pointing at a JSON file |

**HTTP reporting channel**: `deploy-headscale-admin.sh` generates a report token on deployment and stores it in `/var/lib/headscale-admin/node-speed-token` (mode 0600, reused on upgrade). The token can also be supplied directly through `HSA_NODE_SPEED_TOKEN`. **Without a token the whole endpoint is disabled and returns 404**, so it never becomes an anonymous write endpoint. The Settings page has a "Node speed reporting" card showing the endpoint, the token, and the channel state, so an administrator can copy the token and hand it out; the operations page keeps only a channel-state badge and a link to Settings.

```bash
# Single report: node may be the node ID, the node name, or a tailnet IP — all three match
curl -X POST http://<server-tailnet-address>:3001/api/speed \
  -H "X-Node-Speed-Token: <report token>" \
  -H 'Content-Type: application/json' \
  -d '{"node":"100.64.0.5","rx_bytes":154600,"tx_bytes":31220}'

# Arrays are accepted too; the token may also go in the body "token" field or an Authorization: Bearer header
curl -X POST http://<server-tailnet-address>:3001/api/speed \
  -H "Authorization: Bearer <report token>" \
  -d '[{"node":"100.64.0.5","rx_bytes":154600,"tx_bytes":31220}]'
```

`rx_bytes` / `tx_bytes` are **cumulative** byte counts; the server derives the rate from the delta between two reports (the same idea as CPU tick deltas) and displays it as B/s, KB/s, or MB/s. The speed column starts filling in once the node has reported twice.

**Node-side collector**: `node-speed-agent.sh` in this repository is a ready-to-use reference implementation. It finds the `tailscale*` interface, reads the cumulative counters from `/proc/net/dev`, and reports them on an interval:

```bash
HSA_URL=http://<server-tailnet-address>:3001/api/speed \
HSA_TOKEN=<report token> \
bash node-speed-agent.sh
```

Optional environment variables: `HSA_IFACE` (interface to collect), `HSA_INTERVAL` (report interval in seconds, default 10, keep it at 15 or below), `HSA_NODE` (node identifier, defaults to the local tailnet IP), `HSA_ONCE=1` (report once and exit, useful for self-checks).

**Counter file channel**: the `HSA_NODE_SPEED_FILE` environment variable points at a JSON file that a local collector writes cumulative rx/tx byte counts into; the server derives rates from the delta between two samples. The file is keyed by node ID or node name:

```json
{
  "12": {"rx_bytes": 154600, "tx_bytes": 31220},
  "laptop": {"rx_bytes": 1000, "tx_bytes": 500}
}
```

When no channel is connected, every node's speed column shows a `—` placeholder with a hover explanation, and all other monitoring data is unaffected. Counter resets (node or agent restarts) are treated as zero for that interval, never negative. Rates use the same KB/s-based units as the live bandwidth panel.

Only control-plane metrics are available for nodes. Tailscale clients do not report host resources such as CPU and memory to headscale, so per-node CPU/memory is out of scope for this page; if you need it, deploy a collection agent on the nodes, push the data to a new endpoint, and wire it in using the extension points below.

**Alerts**: seven built-in rules — headscale service not active, CPU over threshold, memory over threshold, 1-minute load above cores × multiplier, node offline, node key expired, and node sampling failure. Thresholds can be overridden with environment variables:

| Variable | Default | Meaning |
| --- | --- | --- |
| `HSA_ALERT_CPU` | 85 | CPU usage percentage threshold |
| `HSA_ALERT_MEM` | 90 | Memory usage percentage threshold |
| `HSA_ALERT_LOAD` | 2 | 1-minute load ceiling = CPU cores × this multiplier |

**API**: `GET /api/monitor` requires login and returns four sections — `host`, `nodes`, `alerts`, `thresholds`. The page polls it every 5 seconds; node data is served from the server's 15-second cache, so multiple open pages never hammer the CLI.

**How to extend**:

- New host metric: add a field to `HostSnapshot` in `hostmon.go`, populate it in `sample()` (e.g. read `/proc/mounts` for disk usage), add display text in `newHostView`, and add a meter to the frontend.
- New node metric: pull the field from the CLI JSON in `nodeStatFromMap` in `nodemont.go` and add it to `NodeStat`, then add a column in both the template and `nodeRow` in `static/app.js`; for counter-style metrics that need delta conversion (like bandwidth), follow `nodeSpeedRates` and add a data source.
- New alert rule: implement `func(alertInput, AlertThresholds) []MonitorAlert` in `alerts.go` and register it in the `alertRules` slice — no changes needed to the page or API output; add thresholds to `AlertThresholds` and read them from environment variables in `defaultAlertThresholds`.
- Notification channels: `MonitorAlert`'s `Key` and `Level` are stable, so you can deduplicate by key after rule evaluation and push to email or a webhook.

## Verification

Read-only checks can run on any Linux (including WSL) without creating users, writing to `/etc`, or starting services:

```bash
bash test-headscale-cli.sh        # CLI commands, config keys, and configtest
bash test-headscale-admin.sh      # Admin UI build, login, i18n, themes, bandwidth, host & node monitoring, config validation, IPv6 toggle, network forms, CSRF (requires go/curl)
(cd headscale-admin && go test ./...)   # Unit tests for config parsing, admin UI TLS certificates, node mapping, i18n rendering, and alert rules
```

## Security notes

- The admin UI uses an independent account system: passwords are stored as salted PBKDF2 digests and sessions use HttpOnly cookies; over HTTP the traffic is still plaintext, so on public networks use HTTPS or restrict sources with `--allow-cidr`.
- Initial and reset admin credentials can also be passed through environment variables (`HSA_INIT_ADMIN`, `HSA_RESET_ADMIN`, format `username:password`). Command-line arguments show up in `/proc/<pid>/cmdline` and are readable by any local user; the deploy script uses environment variables by default so the password never lands in the process arguments.
- The deploy script opens TCP/3001 to every source in the firewall. On an internal network add `--allow-cidr` to restrict it; anything exposed publicly must have admin UI HTTPS enabled first.
- Admin UI HTTPS and headscale service HTTPS are two independent configurations and can be enabled separately. Once admin UI HTTPS is on, that port only accepts HTTPS and `http://` requests are not redirected; if a bad certificate makes the page unreachable, delete `admin-tls.json` from the data directory and restart the service to return to HTTP.
- HTTP mode is plaintext; newer Tailscale clients may require `TS_ALLOW_HTTP_CONTROL_SERVER=1`.
- HTTPS mode requires the domain to resolve to this host, with ports 80 (certificate validation) and 443 (service) reachable.
- The built-in DERP relay requires UDP/3478 to be open.

## Upgrading from older versions

Earlier versions also deployed the community project HeadControl (port 3000). All management features are now covered by Headscale Admin, so there is no reason to keep two UIs:

```bash
sudo bash remove-headcontrol.sh          # stop the service, remove program/data, free port 3000, clean firewall rules
sudo bash remove-headcontrol.sh --keep-data   # keep /var/lib/headcontrol
```

The script only removes HeadControl; it does not touch Headscale, Headscale Admin, or `/etc/headscale/config.yaml`. Older versions remain available in the git history.

## License

The scripts, documentation, and admin UI source in this repository are released under the [MIT License](LICENSE).
