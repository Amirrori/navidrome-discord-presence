# Bridge

`nds-bridge` sits between the Navidrome plugin and Discord. It serves the page where people link their Discord account, takes the signed playback events from the plugin, keeps the Discord logins encrypted and starts one `nds-worker` per active user. The worker is what actually holds the connection to Discord through the Social SDK.

- `server/` has `nds-bridge` and the little `nds-test-event` tool (Go, no dependencies outside the standard library)
- `worker/` has `nds-worker` (C++20, needs the Discord Social SDK)
- `deploy/` has a systemd unit, an admin wrapper, a firewall example, a Caddy example and a journald snippet

The bridge serves plain HTTP. It doesn't do HTTPS itself; if you want that, put your own reverse proxy in front.

How to install it is in [../docs/setup.md](../docs/setup.md).

## Settings

Everything is set with environment variables, usually in `/etc/navidrome-discord-status/bridge.env`. Secrets aren't set there. They live in files, and the systemd unit hands them to the bridge as credentials named `discord-client-secret` and `event-secret`. If you don't use systemd, point `NDS_DISCORD_CLIENT_SECRET_FILE` and `NDS_EVENT_SECRET_FILE` at the files instead.

Required:

| Variable | What it is |
|---|---|
| `NDS_PUBLIC_URL` | The address people open for the link page, http or https. `/callback` gets added for the Discord redirect. |
| `NDS_NAVIDROME_URL` | Navidrome's address, used to check logins. Use https if Navidrome has it. |
| `NDS_DISCORD_APP_ID` | The Discord application ID. |
| `NDS_WORKER_PATH` | Where `nds-worker` is. |

Optional:

| Variable | Default | What it does |
|---|---|---|
| `NDS_EVENTS_ADDR` | `127.0.0.1:8090` | Where the bridge listens for the plugin. |
| `NDS_WEB_ADDR` | `127.0.0.1:8091` | Where the link page listens. |
| `NDS_TRUSTED_PROXIES` | none | Only if you put a reverse proxy in front: its IPs (comma separated), so the bridge trusts its `X-Forwarded-For` header for rate limiting. |
| `NDS_STATE_DIR` | `/var/lib/navidrome-discord-status` | Where the encryption key and the links are kept. |
| `NDS_PRIVACY_CONTACT` | none | Shown in the privacy notice. |
| `NDS_LINK_RETENTION_DAYS` | `365` | Links that haven't been used for this many days get removed. `0` keeps them. |
| `NDS_LOG_RETENTION_DAYS` | `14` | Only shown in the privacy notice. Set it to whatever journald keeps. |
| `NDS_DEFAULT_IMAGE_URL` | Navidrome logo | Shown when there's no cover. |
| `NDS_PAUSE_IMAGE_URL` | none | Small icon while paused. |
| `NDS_IMAGE_HOSTS` | Cover Art Archive, archive.org, Deezer, Apple, uguu, GitHub raw | Hosts images may come from. `*.` also matches subdomains. |
| `NDS_LINK_HOSTS` | `open.spotify.com` | Hosts clickable links may point to. |
| `NDS_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |

## Commands

```sh
nds-bridge                       # run it, same as "nds-bridge serve"
nds-bridge links list            # show who is linked
nds-bridge links remove alice    # revoke and delete one link
```

The state directory is locked while the service runs, so the `links` commands only work with it stopped. Run them through `deploy/nds-bridge-admin`, which uses the service's user, settings and credentials. Running `nds-bridge` as root would leave files behind that the service can't read anymore.

```sh
systemctl stop nds-bridge
nds-bridge-admin links list
systemctl start nds-bridge
```

To send a test event without Navidrome:

```sh
nds-test-event -url http://127.0.0.1:8090 -secret-file event-secret -user alice -state playing
```

## Building and tests

```sh
cd server
go test -race ./...
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o nds-bridge ./cmd/nds-bridge

cd ../worker
cmake -S . -B build -DDISCORD_SOCIAL_SDK_DIR=/path/to/discord_social_sdk
cmake --build build
```

`go.mod` pins the Go toolchain, so an older Go just downloads the right version.

The worker's protocol and line reader have their own tests, which build without the SDK:

```sh
cd worker
cmake -S . -B build-tests -DNDS_BUILD_TESTS=ON -DNDS_BUILD_WORKER=OFF
cmake --build build-tests
ctest --test-dir build-tests --output-on-failure
```

There are also fuzz tests for the event parser and the text cleanup. CI runs each one for 20 seconds, locally you can let them run longer:

```sh
cd server
go test -run '^$' -fuzz FuzzDecodeEvent -fuzztime 1m ./internal/events
go test -run '^$' -fuzz FuzzCleanText -fuzztime 1m ./internal/presence
```

## Releases

Pushing a tag that starts with `v` (for example `git tag v0.1.0 && git push origin v0.1.0`) runs the release workflow. It runs the tests and publishes a GitHub release with `nds-bridge` and `nds-test-event` for linux amd64 and arm64, the plugin (`discord-status.ndp`, built for the default private network ranges), the files from `deploy/` and a `SHA256SUMS` file. The worker isn't in it because it contains the Discord SDK, which can't be redistributed, so everyone builds the worker themselves.

## License

MIT, see [LICENSE](LICENSE). The Discord Social SDK has its own terms and isn't included.
