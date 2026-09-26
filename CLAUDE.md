# RouteBox

## Commands
- `make build|test|race|lint|cross` - builds use CGO_ENABLED=0; only `make race` enables cgo
- `go test -race ./...` - full suite; CI runs gofmt, vet, race tests and cross builds on ubuntu + macos
- `go build -o /tmp/rb-bin ./cmd/routebox && /tmp/rb-bin --config "$(mktemp -d /tmp/rbXXXX)/config.json" route …` - CLI smoke test on a throwaway config (offline path; `go run` can't be aliased in zsh)

## Invariants (tests enforce these; run the mutation idea before trusting a green run)
- Proxied routes must never resolve hostnames locally; pass the name to SOCKS as ATYP 0x03 (proxy_test counting-resolver + `*.invalid` checks)
- Unknown/down upstream → 502; never fall back to another upstream or direct
- `direct` is a reserved upstream name; `Config.Normalize` pins empty-via proxy routes to the first upstream; `RemoveUpstream` refuses while routes use it
- All mutations go through `core.App`; the CLI uses the control socket when an instance is running, else the same core code on the config file
- The TUI only sees `tui.Backend`: `*core.App` in-process, or `control.Remote` when `routebox` attaches to a running instance (`q` then detaches only). A new App method used by the TUI needs a control endpoint + `Remote` method; a new event type needs an `encodeEvent`/`decodeEvent` case in `control/wire.go`
- New config fields keep today's behaviour at their zero value (`fallback: ""` = DIRECT, `connection_log_off`) so old configs load unchanged
- Unmatched traffic follows `fallback` under the same rules as proxy routes; `RemoveUpstream` also refuses the fallback upstream and an upstream rename carries it along
- Route keys: `example.com` = the domain and its subdomains; `*.example.com` = subdomains only and beats `example.com`; IPs match exactly
- Read-only data for the TUI/CLI goes on `core.Status`: `/v1/status` and `Remote` polling already carry it, no new endpoint
- Count per-route hits only at the `Attempt` sites in `proxy/connect.go`/`forward.go`; `dialForHTTP` decides again and would double count
- `service install` records the Homebrew link path, not the versioned Cellar path that `brew upgrade` deletes

## Testing patterns
- Fake ssh = the test binary itself via `ROUTEBOX_FAKE_SSH` in `TestMain` (ssh, core packages); fake SOCKS = `internal/socks/sockstest`
- Host-key flows use `internal/ssh/sshtest` (a shell-script ssh with a temp `known_hosts`); never touch the real `~/.ssh`. A changed host key must never be offered for trust
- Unix socket paths max ~104 bytes: use short dirs (`os.MkdirTemp("/tmp", …)`) for sockets in tests
- TUI strings: every `T`/`t`/`toast` literal needs a `ko.go` entry (source-scan test); non-literal keys (badges, preset categories, `core.Warnings`, `core.MigratedNotice`) go in `TestIndirectKeysHaveKorean`
- Layout test must fit every size in both en and ko (Korean is double-width)
- Driving the real TUI in a pty: Bubble Tea's `init` queries the terminal (OSC 11, CSI 6n); the harness must answer or startup stalls and eats keystrokes
- Mutation checks: edit with Python, not BSD `sed` (no-ops on patterns like `\&`), and make sure the mutant compiles; a build error shows `FAIL … [build failed]`, not `--- FAIL`
- Routes panel rows: routes, then the fallback row (`OnFallbackRow`), then "+ Add Route" (`Rows(n) = n+2`)
- Form hints are cut to one line (~46 cells); keep `TextField` hints short in en and ko
- Workflows: no GNU tar/actionlint/docker locally; test on a temp branch with a push-triggered copy, then delete the branch and its runs (`gh run delete`)

## Conventions
- Tests and docs use RFC 2606 names (example.com/org/net); service names appear only in `internal/router/preset.go`
- Commit messages in English
- Pin GitHub Actions by commit SHA
- README.md (English) and README_ko.md (Korean) must stay in sync
- Korean text (ko.go, README_ko.md) attaches particles to Latin words and code: `RouteBox가`, `` `q`는 ``, `%s에`
- Releases: bump every `vX.Y.Z` in both READMEs (`go install …@`, the release tarball example, `--version` example) in a `Release vX.Y.Z` commit; push main, wait for CI, then push the tag. `.github/workflows/release.yml` builds the per-platform tarballs + `SHA256SUMS` onto the GitHub release (`gh workflow run release.yml -f tag=vX.Y.Z` fills in a release that has no assets; it never replaces existing ones). The Homebrew formula (`../homebrew-tap`, github.com/horyu1234/homebrew-tap) bumps itself: its `update-formulae` workflow checks hourly, or right away when the `HOMEBREW_TAP_TOKEN` secret is set; confirm its Actions run passed
- Release tarballs are reproducible (fixed tar mtime/owner, `gzip -n`) and uploaded without `--clobber`: the tap and users trust their sha256
