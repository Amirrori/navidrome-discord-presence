# navidrome discord status

Display music status from navidrome on discord. From any device and any client without the use of your **discord token**.

Every user links their own Discord account once on a small web page, using Discord's normal "authorize this app" screen. After that the server sets their "Listening to" status through Discord's official [Social SDK](https://discord.com/developers/docs/discord-social-sdk/overview). Nobody pastes a token anywhere and no account gets logged in by a script.

## Why not just use a token?

The existing Navidrome plugins log into Discord with your account token. That token gives full access to your account, and Discord treats it as self-botting, so accounts can get disabled for it. Here you only grant one narrow permission (`openid sdk.social_layer_presence`). It can set your status and that's it, no reading or sending messages. You can take it back any time in Discord under User Settings, Authorized Apps.

## How it works

There are three parts:

- `plugin/` is a Navidrome plugin. When a linked user plays, pauses or stops something, it looks up a public cover and sends a signed event to the bridge. (GPL-3.0, it started as a fork of the official Discord Rich Presence plugin.)
- `bridge/server/` is `nds-bridge`. It runs the link page, receives the events, keeps the Discord logins encrypted and starts one worker per active user. (MIT)
- `bridge/worker/` is `nds-worker`. It holds one user's connection to Discord through the Social SDK. (MIT)

```
navidrome + plugin  -->  nds-bridge  -->  nds-worker (one per user)  -->  Discord
users' browsers     -->  nds-bridge (link page)
```

The bridge serves plain HTTP. It doesn't do HTTPS or certificates itself. If you want HTTPS, put your own reverse proxy in front of it; there's an example Caddy config in `bridge/deploy/`.

The details are in [docs/architecture.md](docs/architecture.md).

## What you get

- A "Listening to" status with track, artist, album, cover and a progress bar
- A pause icon while paused, and the status disappears when you stop
- Optional Spotify links on the track and artist
- Covers from the Cover Art Archive, Deezer or iTunes, and optionally uguu.se for the ones nobody else has
- Your own Discord status (online, idle, do not disturb, invisible) is left alone
- Users who haven't linked are ignored completely, nothing about them is sent or looked up
- A privacy notice on the link page, and unlinking deletes the stored link and revokes access at Discord
- A systemd unit, an nftables firewall example and an admin script for running the bridge

## What you need

- Navidrome 0.64 or newer with plugins turned on
- A Linux x86-64 machine for the bridge (glibc 2.31 or newer) that Navidrome can reach
- A Discord application that belongs to a team (Discord requires that for the Social SDK) with the Social SDK turned on, plus the Social SDK for C++. You download that from the Discord developer portal yourself. Discord doesn't allow redistributing it, so it's not in this repo.

## Setting it up

Start with [docs/setup.md](docs/setup.md). If you run it for other people too, have a look at [docs/privacy.md](docs/privacy.md) and [SECURITY.md](SECURITY.md).

## Credits

The plugin started from the [Discord Rich Presence plugin](https://github.com/navidrome/discord-rich-presence-plugin) by the Navidrome team.

This project isn't affiliated with or endorsed by Discord or Navidrome.
