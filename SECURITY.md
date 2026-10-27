# Security

## Reporting a problem

Please don't open a public issue for security problems. Use the "Report a vulnerability" button on the Security tab of this repo instead, and include the version, what you found and how to reproduce it. I'll get back to you as soon as I can.

Only the latest release gets security fixes.

The plugin, the bridge, the worker and the example files in `bridge/deploy` are in scope. Discord's Social SDK and Navidrome itself are other people's projects, report problems there to them.

## What the design already covers

- No Discord user tokens. Users grant one narrow permission that can't read or send messages, and they can revoke it in Discord.
- Requests from the plugin to the bridge are signed with HMAC-SHA256 and can't be replayed.
- Refresh tokens are encrypted on disk, and secrets never end up in command lines or environment variables.
- The link page has CSRF protection, server-side sessions, login rate limits and a strict Content Security Policy.

The longer explanation is in [docs/architecture.md](docs/architecture.md).

## If you run it

- The bridge serves plain HTTP, so sign-ins and the plugin's events aren't encrypted on the way. If people use it over a network you don't trust, add HTTPS with your own reverse proxy (see the end of [docs/setup.md](docs/setup.md)).
- If Navidrome has an https address, use it for `NDS_NAVIDROME_URL`, so passwords aren't sent in plain text. The bridge warns you when they are.
- Build the plugin with `BRIDGE_HOSTS` so it can't reach anything else on your network.
- Use the systemd unit and the nftables example as they are, and check `systemd-analyze security nds-bridge`.
- Keep the secret files readable by root only.
- Turn on two-factor login for everyone in the team that owns the Discord application, and keep Public Client off.
- Never commit the Social SDK, your `bridge.env` or any secrets. Run `pre-commit install` once and gitleaks will check every commit.
- Keep the bridge machine updated, unattended-upgrades is an easy way to do that.
