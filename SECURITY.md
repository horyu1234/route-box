# Security Policy

RouteBox is a local proxy that handles other people's traffic and runs `ssh` on your behalf, so security reports are very welcome.

## Reporting a vulnerability

Please **do not open a public issue**. Use GitHub's private reporting instead:
**Security → Report a vulnerability** on this repository.

Include what you did, what happened, and the RouteBox version (`routebox --version`) and OS. You should get a first response within a week.

## Scope

In scope, for example:

- A proxied route leaking to the local resolver or falling back to a direct connection
- Anything that lets another host use RouteBox when it listens on a loopback address
- Command or option injection into the `ssh` command line
- URLs, query strings, headers or credentials ending up in logs, the TUI or the control API
- Weaknesses in how the config file or control socket are written or permissioned

Out of scope: exposing RouteBox yourself on a non-loopback address (it warns about this and has no authentication by design), and issues in `ssh` or your SOCKS server themselves.
