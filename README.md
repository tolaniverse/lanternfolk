# Lanternfolk

A cozy, multiplayer pixel world where teams and their agents work together:
chat, voice, presence, and shared agents.

Status: **bootstrap**. The server serves health and version endpoints, and
the clients show a placeholder. Slice 1 (the workspace loop: sign in,
workspaces, chat, presence) comes next, terminal client first.

## Layout

| Path | What |
|---|---|
| `cmd/lanternfolkd` | server: one binary for hosted and self-hosted |
| `cmd/lanternfolk-tui` | terminal client ([opentui-go](https://github.com/tolaniverse/opentui-go)) |
| `cmd/lanternfolk-desktop` | desktop client ([Asuka](https://asuka.egoist.dev): native widgets) |
| `protocol/` | the public, versioned wire contract (stdlib only) |
| `internal/core/` | pure domain (stdlib only) |
| `internal/server/` | command bus, gateway, realtime hubs (declares ports) |
| `internal/archtest/` | fails the build if the dependency rule is broken |
| `dev/` | the task runner |
| `tools/` | pinned dev tools (separate module, never linked into product binaries) |

## Develop

All you need is Go. `go.mod` pins the toolchain (`toolchain go1.26.11`),
and the `go` command downloads it if yours is older.

```sh
go run ./dev                 # list tasks
go run ./dev ci              # fmt, vet, staticcheck, tests (-race), 6 cross-builds, govulncheck
go run ./cmd/lanternfolkd -log-format text
go run ./cmd/lanternfolk-tui
go run ./dev desktop -p mac  # or windows, linux → cmd/lanternfolk-desktop/dist
```

Every binary builds with `CGO_ENABLED=0` for linux, darwin, and windows on
amd64 and arm64. Nothing needs a C toolchain, apart from `-race` in tests.

The first `go run ./dev lint|vuln|snapshot|desktop` builds the pinned tools
into `bin/tools/` (a minute or two). After that they are rebuilt only when
`tools/go.mod` changes.

### Server configuration

| Flag | Env | Default |
|---|---|---|
| `-addr` | `LANTERNFOLK_ADDR` | `:8080` |
| `-log-level` | `LANTERNFOLK_LOG_LEVEL` | `info` |
| `-log-format` | `LANTERNFOLK_LOG_FORMAT` | `json` |
| `-shutdown-timeout` | | `20s` |

Endpoints: `GET /healthz` (alive), `GET /readyz` (503 while starting or
draining), `GET /v1/version`.

## CI/CD

- **CI** (`.github/workflows/ci.yml`), on PRs and on `main`: `go run ./dev ci`
  on Linux; vet and tests on macOS and Windows; a goreleaser dry run; on
  `main`, desktop packages for every OS as build artifacts.
- **Release** (`.github/workflows/release.yml`), on a `v*` tag: CLI archives
  and checksums, a multi-arch `lanternfolkd` image built by
  [ko](https://ko.build) (no Dockerfile, distroless base) at
  `ghcr.io/tolaniverse/lanternfolk`, and desktop packages attached to the
  GitHub release.

```sh
git tag v0.1.0 && git push origin v0.1.0
```

Try the release locally without publishing: `go run ./dev snapshot` (writes `dist/`).
