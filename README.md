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
| `cmd/lanternfolk-app` | GUI app ([Asuka](https://asuka.egoist.dev)): native on macOS, Windows, Linux; same code on iOS, Android, web |
| `protocol/` | the public, versioned wire contract (stdlib only) |
| `internal/core/` | pure domain (stdlib only) |
| `internal/server/` | command bus, gateway, realtime hubs (declares ports) |
| `internal/archtest/` | fails the build if the dependency rule is broken |
| `dev/` | the task runner |
| `tools/` | pinned dev tools (separate module, never linked into product binaries) |

## Develop

All you need is Go. `go.mod` pins the toolchain (`go 1.27.0`, `toolchain go1.27.4`),
and the `go` command downloads it if yours is older.

```sh
go run ./dev                 # list tasks
go run ./dev ci              # fmt, vet, staticcheck, tests (-race), 6 cross-builds, govulncheck
go run ./cmd/lanternfolkd -log-format text
go run ./cmd/lanternfolk-tui
go run ./dev asuka dev        # the app on this computer, reloading on save
```

Every binary builds with `CGO_ENABLED=0` for linux, darwin, and windows on
amd64 and arm64, and so do the desktop and web builds of the app. Only the
phone builds need native toolchains: iOS needs Xcode; Android needs a JDK,
the Android SDK, and the NDK. `-race` in tests needs a C compiler.

Tools are pinned and built into `bin/tools/` on first use:

- staticcheck, govulncheck, goreleaser: `tools/go.mod` (a separate module, so they never touch the product's dependencies)
- the asuka CLI: at the asuka version in `go.mod`, because the CLI carries the iOS and Android hosts and must match the library
- actionlint: its own `go install`, because its dependencies conflict with goreleaser's

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
  on Linux; vet and tests on macOS and Windows; a goreleaser dry run. On
  `main` (or run by hand), the app for Windows, Linux, and web (one Linux
  runner), Mac plus an iOS simulator build (macOS runner), and Android with
  a throwaway key, as build artifacts.
- **Release** (`.github/workflows/release.yml`), on a `v*` tag: goreleaser
  makes a **draft** release with CLI archives and checksums, and pushes a
  multi-arch `lanternfolkd` image built by [ko](https://ko.build) (no
  Dockerfile, distroless base) to `ghcr.io/tolaniverse/lanternfolk`. The app
  jobs attach the desktop and web packages, plus TestFlight and Play uploads
  when `MOBILE_RELEASE=true`. The release is published only once all of them
  succeed. Then, with `DEPLOY=true`, `go run ./dev deploy` points the Dokploy
  app at the new image, deploys it, and waits until `/v1/version` reports it.

```sh
git tag v0.1.0 && git push origin v0.1.0
```

Try the release locally without publishing: `go run ./dev snapshot` (writes `dist/`).

## The app on every platform

```sh
go run ./dev asuka build -p mac                # or windows, linux, web
go run ./dev asuka build -p ios --simulator    # needs Xcode
go run ./dev asuka build -p android            # needs JDK + Android SDK + NDK and an upload key
VERSION=v1.2.3 go run ./dev desktop-dist       # all desktop targets, archived into dist/desktop
```

### Release secrets (all optional)

Releases build unsigned when these are missing. Set them in
Settings › Secrets and variables › Actions:

| For | Secrets / variables |
|---|---|
| Mac: signed and notarized | `MAC_CERTIFICATE`, `MAC_CERTIFICATE_PASSWORD`, `APPLE_ID`, `APPLE_APP_PASSWORD`, `APPLE_TEAM_ID`; variable `MAC_SIGN_IDENTITY` |
| Windows: signed `.exe` | `WINDOWS_CERTIFICATE`, `WINDOWS_CERTIFICATE_PASSWORD` |
| iOS → TestFlight | `IOS_CERTIFICATE`, `IOS_CERTIFICATE_PASSWORD`, `IOS_PROFILE`, `ASC_KEY_ID`, `ASC_ISSUER_ID`, `ASC_KEY` |
| Android → Play internal track | `ANDROID_KEYSTORE`, `ANDROID_KEYSTORE_PASSWORD`, `PLAY_SERVICE_ACCOUNT` |
| Turn on store uploads | variable `MOBILE_RELEASE=true` |
| Deploy to Dokploy (CD) | environment `production`: secret `DOKPLOY_API_KEY`; variables `DEPLOY=true`, `DOKPLOY_URL`, `DOKPLOY_APPLICATION_ID`, `PUBLIC_URL` |

See Asuka's [Shipping guide](https://asuka.egoist.dev/docs/shipping) for how to
make each one.

## License

AGPL-3.0-only, except `protocol/`, which is Apache-2.0. See [LICENSING.md](LICENSING.md).
