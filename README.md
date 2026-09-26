# RouteBox

**English** | [한국어](README_ko.md)

**Selective Tunnel Router** — a local HTTP proxy that sends the domains and addresses you choose through the tunnels you choose, and everything else direct.

Point your browser at `127.0.0.1:8080` once. From then on, you decide in a terminal UI which service goes out through which exit — an SSH host in one region, another SSH host somewhere else, a SOCKS5 server you already run, or straight out. Changes apply to the next connection, with no restart, PAC file, or browser extension.

```
 RouteBox                                                                           ● CONNECTED
 Selective Tunnel Router                                          listening on 127.0.0.1:8080
╭──────────────────────────────────────────╮╭──────────────────────────────────────────────────╮
│ ROUTES 4                                 ││ LIVE CONNECTIONS live                            │
│ ▌● example.com                   → seoul ││ 16:30:01 seoul    www.example.com:443   ● open   │
│  ● example.org                   → tokyo ││ 16:30:01 tokyo    cdn.example.org:443   ✓ 2.0s   │
│  ● 203.0.113.10                    → lab ││ 16:30:02 DIRECT   intranet.example.com  ✓ 1.2s   │
│  ○ intranet.example.com           DIRECT ││ 16:30:03 lab      203.0.113.10:443      ● open   │
│  + Add Route                             ││                                                  │
╰──────────────────────────────────────────╯╰──────────────────────────────────────────────────╯
╭──────────────────────────────────────────────────────────────────────────────────────────────╮
│ HTTP 127.0.0.1:8080  ·  up 1h02m  ·  Active 3  Total 120  Proxied 80  Direct 38  Failed 2    │
│ ● seoul        ssh proxy-seoul      SOCKS 127.0.0.1:1080   connected · PID 4121    1 route   │
│ ● tokyo        ssh proxy-tokyo      SOCKS 127.0.0.1:1081   connected · PID 4122    1 route   │
│ ● lab          external SOCKS       SOCKS 127.0.0.1:9050   reachable               1 route   │
╰──────────────────────────────────────────────────────────────────────────────────────────────╯
 [a] add  [v] via  [e] edit  [d] delete  [s] upstreams  [p] preset  [r] restart  [?] help  [q] quit
```

## Contents

1. [What it does](#1-what-it-does)
2. [How it works](#2-how-it-works)
3. [Why](#3-why)
4. [Install](#4-install)
5. [Build](#5-build)
6. [Firefox setup](#6-firefox-setup)
7. [Upstreams: managed SSH](#7-upstreams-managed-ssh)
8. [Upstreams: external SOCKS](#8-upstreams-external-socks)
9. [Routing domains and addresses](#9-routing-domains-and-addresses)
10. [Presets](#10-presets)
11. [TUI keys](#11-tui-keys)
12. [CLI](#12-cli)
13. [Configuration](#13-configuration)
14. [Troubleshooting](#14-troubleshooting)
15. [Security](#15-security)
16. [Architecture](#16-architecture)
17. [License](#license)

## 1. What it does

- Runs a local **HTTP CONNECT proxy** (default `127.0.0.1:8080`).
- Lets you register any number of **upstreams**: managed SSH tunnels (`ssh -N -D`, started and supervised by RouteBox) or existing SOCKS5 servers.
- Lets you register **routes**: a domain (with all of its subdomains) or an exact IPv4/IPv6 address, each sent **via** a specific upstream or `direct`.
- Sends everything that matches no route **direct** by default, or through an upstream you choose.
- Applies changes **immediately** — existing connections keep going, new connections follow the new rules.
- Never decrypts TLS: no certificates, no MITM, no SNI changes. CONNECT tunnels carry opaque bytes.

It does not do VPN, TUN/TAP, packet capture or browser extensions. The design is fixed to **HTTP CONNECT proxy + SOCKS5 + SSH dynamic forwarding**.

## 2. How it works

```
Browser
  │  HTTP CONNECT www.example.com:443
  ▼
RouteBox 127.0.0.1:8080 ── route lookup: example.com → via seoul
  │
  ├── via seoul ──► SOCKS5 127.0.0.1:1080 ── ssh -N -D … proxy-seoul ──► remote DNS + connect ──► site
  ├── via tokyo ──► SOCKS5 127.0.0.1:1081 ── ssh -N -D … proxy-tokyo ──► remote DNS + connect ──► site
  ├── via lab   ──► SOCKS5 127.0.0.1:9050 (a server you run yourself) ─────────────────────────► site
  └── no route / direct ──► OS resolver + direct TCP ────────────────────────────────────────► site
```

- **Matching.** A route for `example.com` covers `example.com`, `www.example.com` and `a.b.example.com`, but not `notexample.com` or `example.com.attacker.net`. RouteBox walks the host's labels and **the most specific route wins**, so `example.com → seoul` plus `intranet.example.com → direct` sends only the intranet direct. A **wildcard** route `*.example.com` covers only the subdomains (`www.example.com`, `a.b.example.com`), not `example.com` itself; next to an `example.com` route it is the more specific one, so `example.com → direct` plus `*.example.com → seoul` splits the domain from its subdomains. Wildcards go only at the front (`api.*.example.com` is rejected). IP routes match that exact address only.
- **DNS stays remote.** Proxied connections never touch the local resolver. The hostname goes to the SOCKS server as a domain name (ATYP `0x03`), so it is resolved on the far side of the tunnel. Local DNS filtering or tampering does not affect routed domains. Tests pin this down (see [Architecture](#16-architecture)).
- **Fail closed.** If a route's upstream is down or has been removed, the client gets `502 Bad Gateway`. Traffic never silently falls back to another upstream or to direct. The same holds for unmatched traffic when **everything else** goes through an upstream.

## 3. Why

- Different services need different exits: one region for one service, another region for another, a lab network for internal hosts, and everything else local.
- Local DNS blocks or rewrites certain names, and you want those names resolved by the remote end only.
- You'd rather change routing from a terminal than hand-edit PAC files and reload the browser.
- All you have is SSH access. `ssh -D` is enough; nothing has to be installed on the server.

## 4. Install

With Go (see `go.mod` for the required version), install the latest release tag:

```sh
go install github.com/horyu1234/route-box/cmd/routebox@v0.2.0
```

Always name the tag: `@latest` goes through the Go module proxy, which can keep serving the previous version for a while after a release. Newer releases are listed on the [tags page](https://github.com/horyu1234/route-box/tags); `routebox --version` prints the one you have.

Or build from source ([Build](#5-build)). The result is a single static binary (`CGO_ENABLED=0`); copy it anywhere on your `PATH`.

If macOS Gatekeeper blocks a downloaded binary: `xattr -d com.apple.quarantine routebox`.

### Run in the background

Once Firefox points at RouteBox, **every** site fails with "The proxy server is refusing connections" while RouteBox is not running. Register it as a per-user service so it starts at login and restarts if it crashes:

```sh
routebox service install     # macOS: launchd LaunchAgent · Linux: systemd --user unit
```

After that, running `routebox` opens the TUI as a **management panel** for the background instance: every change goes to it over the control socket, and `q` closes only the panel while the proxy and ssh tunnels keep running. The CLI commands (`route`, `upstream`, `status`, …) talk to it the same way.

```sh
routebox service status      # installed? running? where is the log?
routebox service restart     # e.g. after replacing the binary
routebox service stop        # until the next login (or `routebox service start`)
routebox service uninstall   # stop and remove from login
```

- The service runs `routebox --no-tui --config <absolute path>` with the config path and `PATH` of the shell you ran `install` from. If you use `--config` or `ROUTEBOX_CONFIG`, pass the same one to `install` and to `routebox` when you attach. Run `install` again after moving the binary or the config.
- Logs: macOS writes `~/Library/Logs/RouteBox/routebox.log` (rotated at 10 MB, one `.1` backup kept); Linux uses the journal (`journalctl --user -u routebox`).
- If RouteBox is already running in a terminal when you install, the service keeps retrying and takes over within about 10 seconds after you quit it. Each retry adds an `another RouteBox instance is already running` line to `~/Library/Logs/RouteBox/stderr.log` on macOS, so don't leave a terminal instance running alongside the service.
- ssh runs without a terminal, so it cannot ask for a passphrase or confirm a host key. Use keys that are in an agent (macOS: `UseKeychain yes` + `AddKeysToAgent yes`, or an `IdentityAgent` such as 1Password) and trust each server's host key once (`t` in the upstream manager or `routebox ssh trust <upstream>`, see [Host keys](#host-keys)). On Linux, `systemd --user` services do not inherit `SSH_AUTH_SOCK` from your shell; see [Troubleshooting](#14-troubleshooting).
- A process manager or login item of your own works too: run `routebox --no-tui` (optionally with `--log-file`).

### Updating

1. Quit the running instance: `q` in the TUI, or `Ctrl+C` for `--no-tui`. `routebox status` should print `RouteBox   not running`. With the [background service](#run-in-the-background) you can skip this step.
2. Optionally back up `config.json` ([Configuration](#13-configuration)).
3. Replace the binary the same way you installed it, naming the new tag from the [tags page](https://github.com/horyu1234/route-box/tags):

   ```sh
   go install github.com/horyu1234/route-box/cmd/routebox@v0.2.0
   # or, from a source checkout:
   git pull && make build   # then copy bin/routebox over the old binary
   ```

4. Start `routebox` again, or run `routebox service restart` for the background service (it keeps running the old binary until restarted). Routes and upstreams are kept in the config file, and configs from older versions are migrated when loaded.

`routebox --version` shows the installed version: the module version for `go install` (e.g. `v0.2.0`), or `git describe` for `make build`.

## 5. Build

```sh
make build   # bin/routebox
make run     # build and run (pass flags with ARGS="--no-tui")
make test    # go test ./...
make race    # go test -race ./...
make lint    # gofmt check + go vet (+ golangci-lint if installed)
make cross   # dist/: darwin/arm64, darwin/amd64, linux/amd64, linux/arm64
make clean
```

Every build target uses `CGO_ENABLED=0`; only `make race` enables cgo, because the race detector needs it.

## 6. Firefox setup

1. **Settings** → **General** → scroll to **Network Settings** → **Settings…**
2. Choose **Manual proxy configuration**.
3. **HTTP Proxy** `127.0.0.1`, **Port** `8080`.
4. Make HTTPS use the same proxy. Depending on the version this is a checkbox labelled **"Also use this proxy for HTTPS"** or, in older versions, **"Use this proxy server for all protocols"**. If there is a separate HTTPS Proxy field instead, enter `127.0.0.1` / `8080` there too.
5. Leave **SOCKS Host** empty — RouteBox handles SOCKS itself.
6. **OK**.

Firefox sends HTTPS as `CONNECT host:443`, so RouteBox routes on the hostname alone. Wording varies between Firefox versions and languages.

> While RouteBox is not running, Firefox cannot load any page through this proxy. Install the [background service](#run-in-the-background) so it is always there.

> Setting the proxy only in Firefox keeps other apps out of RouteBox. To cover every app that honours the system proxy, set the same address in your OS proxy settings instead (macOS: System Settings → Network → Details → Proxies → Web Proxy (HTTP) and Secure Web Proxy (HTTPS)).

## 7. Upstreams: managed SSH

RouteBox starts and supervises one `ssh -N -D` process per managed upstream.

On first launch the TUI asks for your first upstream. Afterwards press **`s`** to open the upstream manager (`a` add, `e` edit, `d` delete, `r` restart, `t` check the host key).

| Field | Meaning |
|---|---|
| Type | `managed` |
| Name | Used by routes (`seoul`, `work`, `lab` …). Leave empty to derive it from the host. `direct` is reserved. |
| SSH Host | A `Host` alias from `~/.ssh/config`, or a hostname |
| SSH User / SSH Port | Optional; empty means "whatever `~/.ssh/config` says" |
| Identity File | Optional. Empty means ssh chooses: `IdentityFile` from `~/.ssh/config`, the agent's keys, then the default `~/.ssh/id_*`. The placeholder is only an example. Only the **path** is stored, never the key |
| Reconnect | Restart ssh when it exits: a dropped tunnel retries with exponential backoff (1s → 30s); a rejected login, an untrusted host key or a server refusing connections waits 30s from the first retry, because retrying sooner cannot help and repeated failed logins get your IP blocked by sshd |
| Local SOCKS | Where `ssh -D` listens. Must be different for every upstream; the next free `127.0.0.1:10xx` is suggested |

The command RouteBox runs:

```sh
ssh -N -D 127.0.0.1:1080 \
    -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
    -o BatchMode=yes -o ConnectTimeout=10 \
    [-p PORT] [-l USER] [-i IDENTITY] -- HOST
```

- Empty fields are not passed, so `User`, `Port`, `IdentityFile`, `ProxyJump` and friends from `~/.ssh/config` keep working.
- **Public-key authentication only.** `BatchMode=yes` makes ssh fail instead of prompting for a password or a host key. Any key ssh itself can use works: the **Identity File** field (`-i`), `IdentityFile` in `~/.ssh/config`, the default `~/.ssh/id_*` keys, and keys held by an agent — `ssh-agent` via `SSH_AUTH_SOCK`, or whatever `IdentityAgent` in `~/.ssh/config` points to (1Password, Secretive, …). Passphrase-protected keys must be loaded into an agent first (`ssh-add`), because ssh cannot ask for the passphrase.
- ssh opens the `-D` port only after authentication, so an upstream counts as connected once a SOCKS greeting succeeds.
- ssh's stderr shows up in TUI toasts, the upstream manager and `routebox ssh status`.
- On exit, every ssh child gets SIGTERM (SIGKILL after 3 s) and is reaped — no zombies.

### Host keys

ssh runs without a terminal, so it cannot ask "Are you sure you want to continue connecting?" for a server it has never seen, and the tunnel fails with `Host key verification failed`. RouteBox then shows a toast, and in the upstream manager (`s`) **`t`** checks the host key:

1. RouteBox connects with ssh itself (following `~/.ssh/config`: `Port`, `HostKeyAlias`, `HashKnownHosts`, …) with authentication disabled, and shows the key type and `SHA256:` fingerprint the server presented.
2. Compare it with the fingerprint on the server, e.g. `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`.
3. `y` appends exactly that key, as ssh itself wrote it, to your `known_hosts` (the first `UserKnownHostsFile`) and reconnects. `n` saves nothing.

`routebox ssh trust <upstream>` does the same from the command line. If the key has **changed** since it was saved, RouteBox refuses and tells you to remove the old key with `ssh-keygen -R <host>` after confirming the new one, because a changed key is what a man-in-the-middle looks like. With `ProxyJump`, the jump host's key must already be trusted. Running `ssh <host>` once in a terminal also works.

Example `~/.ssh/config`:

```
Host proxy-seoul
    HostName 203.0.113.10
    User ubuntu
    IdentityFile ~/.ssh/id_ed25519

Host proxy-tokyo
    HostName 198.51.100.20
    User ubuntu
```

## 8. Upstreams: external SOCKS

Use a SOCKS5 server you already run — an `ssh -D` you started yourself, or any no-auth SOCKS5 server:

```sh
ssh -N -D 127.0.0.1:9050 user@lab-gateway
routebox upstream add lab --external --socks 127.0.0.1:9050
```

RouteBox does not touch ssh for external upstreams; it only checks every 5 seconds that the SOCKS server answers.

`--socks` on the command line overrides the SOCKS address of the upstream that is **first when RouteBox starts**, for that run only (it is not saved). It stays with that upstream: removing it does not move the override to another upstream, and editing it drops the override.

## 9. Routing domains and addresses

In the TUI:

- **`a`** — add a route. Type a domain, an IP address, or paste a whole URL: `https://WWW.Example.com:443/watch?v=1` is stored as `www.example.com` (scheme, path, query and port removed, lower-cased, trailing dot removed). Start with `*.` to match only subdomains. Pick **Via** with ←/→.
- **`v`** (or space) — send the selected route to the next upstream; after the last upstream comes `direct`. The change applies immediately.
- **`e`** — edit domain and via. **`d`** — delete.
- **everything else** — the row below the routes: where traffic that matches no route goes. `DIRECT` by default; `v` (or `enter`) sends it to the next upstream, then back to `direct`. An upstream used here cannot be deleted until you change it.
- Each route shows where it goes (`→ seoul`, `DIRECT`), coloured by that upstream's health, followed by its hit count: how many connections and requests it matched since RouteBox started (`12k` = 12,000+). A subdomain request counts toward the route that matched it; counts reset on restart, when the route is removed or its domain is edited, but survive `v`.

From the CLI:

```sh
routebox route add example.com --via seoul
routebox route add 203.0.113.10 --via lab
routebox route add intranet.example.com --via direct   # exception inside example.com
routebox route add '*.example.org' --via seoul         # subdomains only, not example.org
routebox route via example.com tokyo                   # move a route
routebox route default seoul                           # unmatched traffic via seoul (`direct` to undo)
routebox route list                                    # HITS column while RouteBox is running
```

A route added before any upstream exists is attached to the first upstream you create. After that, routes always name their upstream explicitly, so reordering upstreams never reroutes anything. An upstream that routes still use cannot be deleted — move those routes first (`v`).

## 10. Presets

Presets add a group of related domains in one step, sent via the upstream you choose.

```sh
routebox preset list
routebox preset add <name> --via seoul
routebox --preset <name>        # add before starting
# TUI: p → pick a preset → pick the upstream
```

| Category | Presets |
|---|---|
| OTT / MEDIA | streaming and music services |
| AI | AI assistants |
| DEV | code hosting and package registries |
| UTILITY | `ipcheck` — "what is my IP" services, handy for checking that a tunnel works |

`routebox preset list` prints every preset with its domains. Presets list the domains a service is known to use, but services change their CDNs. If something still goes direct, watch **Live Connections** for `DIRECT` entries while you use the service and add the missing domains with `a`.

## 11. TUI keys

| Key | Action |
|---|---|
| `↑`/`k`, `↓`/`j` | Move selection (scroll the log when it has focus) |
| `tab` | Switch focus between Routes and Live Connections |
| `a` | Add a route |
| `v` / space | Send the selected route to the next upstream (on **everything else**: where unmatched traffic goes) |
| `e` / `enter` | Edit the selected route (`enter` on "+ Add Route" adds) |
| `d` | Delete the selected route (confirm with `y`) |
| `p` | Add a preset |
| `s` | Upstream manager (`a` add, `e` edit, `d` delete, `r` restart, `t` check host key) |
| `r` | Restart all upstreams |
| `l` | Show/hide the log (on narrow terminals: switch Routes ↔ log) |
| `c` | Clear the connection log |
| `o` | Turn connection logging off / on, saved to the config |
| `L` | Switch language (English ↔ 한국어), saved to the config |
| `g` / `G` | Top / bottom |
| `?` | Help |
| `esc` | Close a dialog |
| `q`, `ctrl+c` | Quit (stops ssh tunnels; press again to quit immediately). When attached to a [background instance](#run-in-the-background), closes only the panel |

The layout adapts to the terminal: side-by-side panels from 96 columns, Routes first below that (`l` switches to the log), a one-line footer below 22 rows, and a "Terminal too small" notice below 50×14.

**Language.** The TUI speaks English and Korean. It uses, in order: `--lang`, the saved `language` setting, then `LC_ALL` / `LC_MESSAGES` / `LANG` (`ko_*` → Korean). Press `L` to switch and save.

## 12. CLI

```sh
routebox                                   # TUI (attaches as a management panel if RouteBox is already running)
routebox --no-tui                          # headless; logs events to stderr
routebox --no-tui --log-file PATH          # headless; log to a file rotated at 10 MB
routebox --listen 127.0.0.1:8080           # this run only (not saved)
routebox --socks 127.0.0.1:1081            # first upstream's SOCKS, this run only
routebox --lang ko                         # TUI language, this run only

routebox upstream add seoul --host proxy-seoul [--user U --port N --identity PATH --socks ADDR --no-reconnect]
routebox upstream add lab --external --socks 127.0.0.1:9050
routebox upstream list                     # --json
routebox upstream remove lab

routebox route add example.com --via seoul
routebox route via example.com tokyo
routebox route default [upstream|direct]   # where unmatched traffic goes
routebox route remove example.com
routebox route list                        # --json

routebox preset list
routebox preset add <name> --via seoul

routebox status                            # --json; exit code 1 when not running
routebox log off|on                        # stop / resume recording connections (saved)
routebox log clear                         # forget the connections recorded so far
routebox ssh status [upstream]             # state + recent ssh stderr
routebox ssh restart [upstream]
routebox ssh trust <upstream>              # show the host key fingerprint, save after you confirm

routebox service install|uninstall         # run at login (launchd / systemd --user)
routebox service start|stop|restart|status
```

The CLI and the TUI share the same core (`internal/core`). When RouteBox is running, the CLI talks to it over a control socket and changes apply immediately. When it is not, the CLI edits the config file through the same code and changes apply on the next start. A second instance using the same config directory refuses to start, which also prevents duplicate ssh processes; running `routebox` (the TUI) instead attaches to the running instance, and `--listen`/`--socks` are rejected because they only apply at start.

## 13. Configuration

Located with `os.UserConfigDir()`:

| OS | Path |
|---|---|
| macOS | `~/Library/Application Support/routebox/config.json` |
| Linux | `~/.config/routebox/config.json` (honours `$XDG_CONFIG_HOME`) |

Override with `--config <path>` or `ROUTEBOX_CONFIG`.

```json
{
  "listen": "127.0.0.1:8080",
  "language": "en",
  "upstreams": [
    { "name": "seoul", "mode": "managed", "host": "proxy-seoul", "socks": "127.0.0.1:1080", "reconnect": true },
    { "name": "tokyo", "mode": "managed", "host": "proxy-tokyo", "user": "me", "port": 2222,
      "identity_file": "~/.ssh/id_ed25519", "socks": "127.0.0.1:1081", "reconnect": true },
    { "name": "lab", "mode": "external", "socks": "127.0.0.1:9050", "reconnect": false }
  ],
  "routes": [
    { "domain": "example.com", "mode": "proxy", "upstream": "seoul" },
    { "domain": "example.org", "mode": "proxy", "upstream": "tokyo" },
    { "domain": "203.0.113.10", "mode": "proxy", "upstream": "lab" },
    { "domain": "intranet.example.com", "mode": "direct" }
  ],
  "fallback": "seoul",
  "connection_log_off": false
}
```

- `fallback` is the upstream for traffic that matches no route. Omit it (or use `"direct"`) to send that traffic direct.
- `connection_log_off: true` stops recording connections (`o` in the TUI, `routebox log off`); traffic stats and hit counts keep counting.
- Omitted `user`/`port`/`identity_file` defer to `~/.ssh/config`. Setting `"port": 22` passes `-p 22` and overrides it.
- Every change is saved immediately with an **atomic write**: temp file in the same directory, fsync, rename. The file is `0600`, the directory `0700`.
- If the file cannot be loaded, RouteBox **never deletes or overwrites it**. The TUI shows the error and offers to continue with safe defaults, backing the file up to `config.json.bak-YYYYMMDD-HHMMSS` first. `--no-tui` and the CLI print the error and exit.
- Configs from older versions with a single top-level `socks`/`ssh` are migrated to one upstream named `default` when loaded.
- The control socket is `routebox.sock` in the same directory (`0600`). If that path is too long for a unix socket, `routebox-<uid>.sock` in the temp directory is used.

## 14. Troubleshooting

| Symptom | Cause and fix |
|---|---|
| Firefox: "The proxy server is refusing connections" on every site | RouteBox is not running. Start it, or install the [background service](#run-in-the-background). |
| Tunnel works from the TUI but fails as a service | The service has no terminal: a passphrase-protected key not in an agent, or an unknown host key, cannot be answered. Trust the host key with `t` / `routebox ssh trust`; see [Run in the background](#run-in-the-background) and check the service log. |
| Service on Linux: `Permission denied (publickey)` | `systemd --user` does not see your shell's `SSH_AUTH_SOCK`. Set `IdentityAgent` in `~/.ssh/config`, or run `systemctl --user import-environment SSH_AUTH_SOCK` in your login session and `routebox service restart`. |
| `Host key verification failed` | The server's host key is not in `known_hosts` yet. Press `s`, select the upstream, press `t`, compare the fingerprint and press `y` (or `routebox ssh trust <upstream>`). See [Host keys](#host-keys). |
| `Permission denied (publickey)` | Key not authorised on the server, or a passphrase-protected key is not in the agent. `ssh-add ~/.ssh/id_ed25519`, then `r`. |
| `Connection closed by <ip> port <n>` (server log: `drop connection … penalty`) | sshd (OpenSSH 9.8+ `PerSourcePenalties`, or fail2ban) is temporarily refusing your IP after failed logins. It clears by itself within minutes; fix the login first. If the server has many keys offered to it, set `IdentitiesOnly yes` and the right `IdentityFile` for this host. On a server you run, `PerSourcePenaltyExemptList` can exempt a fixed client IP. |
| `Permission denied (publickey)` although the key is in `ssh-agent` | `~/.ssh/config` sets `IdentityAgent` (e.g. 1Password), so ssh asks that agent instead of `SSH_AUTH_SOCK`. Add the key to that agent, or set `IdentityAgent` for this host. `ssh -v <host>` shows which agent and keys are tried. |
| `Could not resolve hostname` | Typo in the host or missing `~/.ssh/config` entry. Check with `ssh -G <host>`. |
| `SOCKS port already in use` | Another `ssh -D` already listens there. Add it as an external upstream, or give this upstream a different Local SOCKS address. |
| `SOCKS address already used by another upstream` | Each upstream needs its own SOCKS address. |
| `listen 127.0.0.1:8080: address already in use` | Another app owns 8080. Use `--listen 127.0.0.1:8081` or change `listen`, and update Firefox. |
| `another RouteBox instance is already running` | Check with `routebox status`, or stop the other instance. |
| `upstream is still in use` | Move the routes to another upstream (`v`, or `routebox route via`) before deleting it. If it is where unmatched traffic goes, change that first (`v` on **everything else**, or `routebox route default`). |
| A routed site returns `502` | That route's upstream is down. The badge shows `DEGRADED`/`RECONNECTING` and the footer shows which upstream. RouteBox does not fall back on purpose. |
| A site seems to ignore a new route | The browser is reusing an existing connection. Rules apply to **new** connections; reload the tab or wait. Also check step 4 of the Firefox setup. |
| Some parts of a service still go direct | The service uses more domains than the route covers. Watch Live Connections for `DIRECT` entries and add them. |

Status badge:

| Badge | Meaning |
|---|---|
| `CONNECTED` | Proxy running and every upstream healthy |
| `DEGRADED` | Proxy running, at least one upstream unhealthy (other routes keep working) |
| `RECONNECTING` | A managed ssh is connecting or reconnecting |
| `DISCONNECTED` | No upstream configured, or the proxy is not running |

## 15. Security

- The default listen address is `127.0.0.1`. Binding to `0.0.0.0` or any non-loopback address shows **"RouteBox is listening on a non-loopback address. This may expose an open proxy to your network."** in the TUI and the CLI. RouteBox has no authentication; exposing it turns your SSH hosts into an **open proxy**.
- TLS is never decrypted; no certificates are created or installed; SNI is untouched.
- Logs, the TUI and events record **`host:port` only** — never URL paths, query strings or headers (Authorization, Proxy-Authorization, Cookie). Plain-HTTP forwarding drops hop-by-hop headers such as `Proxy-Authorization`.
- Private keys are never stored, only their path. There is no password-auth UI.
- Host keys are never accepted automatically: RouteBox shows the fingerprint and saves the key only after you confirm it, and never offers to trust a key that has changed.
- ssh hosts and users starting with `-` are rejected, and `--` precedes the host to prevent option injection.
- The config file (`0600`) and control socket (`0600` in a `0700` directory) are private to your account. Anyone who can use the control socket controls RouteBox, which is why it is not exposed over TCP.
- The background service on macOS logs every connection's `host:port` to `~/Library/Logs/RouteBox/routebox.log` (`0600`, rotated at 10 MB with one backup). `routebox service uninstall` leaves the log in place; delete the folder yourself if you stop using the service.
- To keep no record of where you connect, turn the connection log off (`o` in the TUI or `routebox log off`). RouteBox then records no connections in the TUI, over the control socket, or in the `--no-tui` / `--log-file` / service log; only counts remain. `c` or `routebox log clear` forgets what is already in memory (it does not edit log files).
- Keep in mind: browser features that bypass the proxy (DNS-over-HTTPS, prefetching) and apps that ignore proxy settings do not go through RouteBox. UDP traffic such as WebRTC cannot pass through an HTTP proxy.

## 16. Architecture

```
cmd/routebox/          Cobra CLI; assembles TUI / --no-tui / attached modes; control-socket client
internal/
  core/                App: ties config, router, proxy, per-upstream ssh, stats, events together
                       (TUI, CLI and control socket all call the same methods)
  config/              Config/Upstream types, validation, legacy migration, atomic save, Store
  router/              input normalisation, label-walk matcher (most specific wins), via, presets
  proxy/               HTTP CONNECT / plain-HTTP server, per-upstream dialing, bidirectional relay
  socks/               minimal SOCKS5 client (hostnames always as ATYP 0x03)
    sockstest/         in-process SOCKS5 server for tests (records addresses exactly as received)
  ssh/                 supervises one ssh child: readiness, reconnect, termination and reaping
  control/             HTTP API over a unix socket + single-instance lock; NDJSON event stream;
                       Remote (snapshot-polling Backend for a TUI attached to a running instance)
  service/             launchd LaunchAgent / systemd --user unit generation and control
  logfile/             size-rotated log file for the background service
  events/              non-blocking event bus (a slow subscriber loses events; the proxy never blocks)
  stats/               atomic counters
  logbuf/              generic ring buffer (last 500 connections)
  tui/                 Bubble Tea model / update / view
    components/        props-in, string-out presentational components
    i18n/              English / Korean strings
```

```
          ┌──────────── TUI ────────────┐      ┌──── CLI ─────┐
          │ Bubble Tea (events + polls) │      │ route/…/ssh  │
          └──────────────┬──────────────┘      └──────┬───────┘
                         │ calls or unix socket       │ unix socket (running)
                         ▼                            ▼ or direct calls (not running)
 ┌──────────────────────────────── core.App ────────────────────────────────┐
 │ config.Store ─► router.Router (atomic swap)                                │
 │ proxy.Server ─► proxy.Transport ─┬─ direct     : net.Dialer (OS resolver) │
 │                                  └─ via <name> : socks.Dialer → that SOCKS│
 │ ssh.Manager × N (one per managed upstream)   health probe per upstream    │
 │ stats.Stats (atomic)   events.Bus ─► subscribers   logbuf.Ring            │
 └───────────────────────────────────────────────────────────────────────────┘
```

Design choices:

- The routing table is an **immutable snapshot** swapped through `atomic.Pointer`; lookups are lock-free and a change is visible to the next connection.
- Bytes the client sends **right after the CONNECT header** (typically the TLS ClientHello) are forwarded, not dropped.
- **Half-close**: when one side sends EOF only the other side's write half is closed, and the other direction keeps flowing. The SOCKS client therefore returns the raw TCP connection, which supports `CloseWrite`.
- The **idle timeout** uses one shared "last activity" timestamp and a watchdog instead of per-direction read deadlines, so a download-only stream is not cut off for having no upload.
- **Graceful shutdown** stops accepting, gives in-flight tunnels a 1 s grace period, closes the rest and waits for every handler goroutine. The process exits only after every ssh child has been reaped.

Properties the tests pin down:

- Proxied connections never call the local resolver and pass the hostname to SOCKS unchanged: a counting resolver injected into the direct dialer sees **0 lookups for proxied routes and at least 1 for direct ones** (the positive control), and a `*.invalid` name that cannot resolve locally still connects through SOCKS.
- **Each route reaches only its own upstream**: with two SOCKS servers, each receives only its own hosts, and a route pointing at an unknown upstream gets `502` without touching either.
- No bytes lost after the CONNECT header, half-close, idle timeout, graceful shutdown, abrupt client/upstream disconnects, IPv6 literals, CONNECT without a port, malformed requests, busy ports.
- ssh supervision runs the test binary itself as a fake `ssh` to verify connect, auth failure, reconnect, restart, SIGKILL escalation when SIGTERM is ignored, **no zombies**, and that adding or removing an upstream at runtime starts or stops exactly that upstream's ssh.
- Every TUI string has a Korean translation with the same format verbs, and every layout fits the terminal in both languages.

Known limitations:

- macOS has no equivalent of Linux's `Pdeathsig`, so if RouteBox is killed with `kill -9` its ssh children may survive. Normal exit, SIGTERM, SIGINT and SIGHUP always clean up.
- Plain-HTTP `Upgrade` (unencrypted `ws://`) is not supported. HTTPS and `wss://` use CONNECT and work.
- SOCKS5 username/password authentication is not supported (no-auth only, which is what `ssh -D` provides).

## License

[MIT](LICENSE). Release binaries also include third-party code under MIT, BSD-3-Clause and Apache-2.0 licenses (Charmbracelet libraries, Cobra, golang.org/x). Security issues: see [SECURITY.md](SECURITY.md).
