# RouteBox

## Commands
- `make build|test|race|lint|cross` - builds use CGO_ENABLED=0; only `make race` enables cgo
- `go test -race ./...` - full suite; CI runs gofmt, vet, race tests and cross builds on ubuntu + macos

## Invariants (tests enforce these; run the mutation idea before trusting a green run)
- Proxied routes must never resolve hostnames locally; pass the name to SOCKS as ATYP 0x03 (proxy_test counting-resolver + `*.invalid` checks)
- Unknown/down upstream → 502; never fall back to another upstream or direct
- `direct` is a reserved upstream name; `Config.Normalize` pins empty-via proxy routes to the first upstream; `RemoveUpstream` refuses while routes use it
- All mutations go through `core.App`; the CLI uses the control socket when an instance is running, else the same core code on the config file
- The TUI only sees `tui.Backend`: `*core.App` in-process, or `control.Remote` when `routebox` attaches to a running instance (`q` then detaches only). A new App method used by the TUI needs a control endpoint + `Remote` method; a new event type needs an `encodeEvent`/`decodeEvent` case in `control/wire.go`

## Testing patterns
- Fake ssh = the test binary itself via `ROUTEBOX_FAKE_SSH` in `TestMain` (ssh, core packages); fake SOCKS = `internal/socks/sockstest`
- Host-key flows use `internal/ssh/sshtest` (a shell-script ssh with a temp `known_hosts`); never touch the real `~/.ssh`. A changed host key must never be offered for trust
- Unix socket paths max ~104 bytes: use short dirs (`os.MkdirTemp("/tmp", …)`) for sockets in tests
- TUI strings: every `T`/`t`/`toast` literal needs a `ko.go` entry (source-scan test); non-literal keys (badges, preset categories, `core.Warnings`, `core.MigratedNotice`) go in `TestIndirectKeysHaveKorean`
- Layout test must fit every size in both en and ko (Korean is double-width)
- Driving the real TUI in a pty: Bubble Tea's `init` queries the terminal (OSC 11, CSI 6n); the harness must answer or startup stalls and eats keystrokes

## Conventions
- Tests and docs use RFC 2606 names (example.com/org/net); service names appear only in `internal/router/preset.go`
- Commit messages in English
- Pin GitHub Actions by commit SHA
- README.md (English) and README_ko.md (Korean) must stay in sync
- Korean text (ko.go, README_ko.md) attaches particles to Latin words and code: `RouteBox가`, `` `q`는 ``, `%s에`
- Releases: bump every `vX.Y.Z` in both READMEs (`go install …@`, the release tarball example, `--version` example) in a `Release vX.Y.Z` commit; push main, wait for CI, then push the tag. `.github/workflows/release.yml` builds the per-platform tarballs + `SHA256SUMS` onto the GitHub release (`gh workflow run release.yml -f tag=vX.Y.Z` rebuilds). Then bump the four `url`/`sha256` pairs in `../homebrew-tap/Formula/routebox.rb` (github.com/horyu1234/homebrew-tap) from `SHA256SUMS` and run `brew audit --strict --online` + `brew reinstall` + `brew test`
