# Discord Status plugin for Navidrome

This plugin tells a [navidrome-discord-status bridge](../bridge) what each user is playing, and the bridge turns that into a "Listening to" status on their Discord account.

The plugin never sees Discord logins. People link their own account on the bridge's page, through Discord's normal authorization screen.

## How it works

The plugin first asks the bridge whether a user has linked Discord and remembers the answer for a few minutes. For anyone who hasn't, it does nothing at all: no cover lookups, no uploads, no events.

For users who have, Navidrome reports every play, pause and stop. The plugin finds a public cover (and Spotify links if you want them) and sends a small JSON event to the bridge, signed with HMAC-SHA256 over a timestamp, a nonce and the body. The bridge ignores anything unsigned, changed or replayed. If the bridge is down, playback isn't affected, the plugin just logs it.

## Covers

Discord loads the cover image itself, so it needs a public link. The plugin tries these in order, and you can turn each one off:

1. The Cover Art Archive, using the MusicBrainz release ID (on by default)
2. Deezer, then iTunes, searching by artist and album (on by default). A result is only used if both artist and album match.
3. Uploading the cover from your library to uguu.se, a public file host that deletes files after about 3 hours (off by default)

If none of them has a cover, the bridge shows a default image.

## Settings

- **Bridge URL**: the bridge's events address, e.g. `http://192.0.2.17:8090` (or your reverse proxy's address if you added one)
- **Bridge secret**: the bridge's event secret, in `/etc/navidrome-discord-status/credentials/event-secret` on the bridge machine (at least 32 characters)
- **Cover Art Archive, Deezer/iTunes, uguu.se**: the cover sources above
- **Spotify links**: makes the track and artist clickable, looked up through ListenBrainz

## Permissions

The plugin asks Navidrome for:

- `http`, to reach the bridge and the services above. By default the manifest allows every private address range, because it can't know where your bridge is. Build it with `BRIDGE_HOSTS` (see below) and it can only reach your bridge. You need that anyway if your bridge URL uses a host name, because Navidrome only accepts host names that are listed in the manifest.
- `users`, to get playback reports
- `cache`, for the cover and link lookups
- `subsonicapi`, to read the cover for the optional uguu.se upload

## Building

You need Go and [TinyGo](https://tinygo.org). Release builds use TinyGo 0.43.0 with Go 1.26.10 or newer.

```sh
make test       # run the tests
make package    # builds discord-status.ndp

# better: only allow your bridge (its IP, or its host name plus the IP it resolves to)
make package BRIDGE_HOSTS=192.0.2.17
```

Copy `discord-status.ndp` into Navidrome's plugins folder, enable it under Settings, Plugins and fill in the settings.

## License

GPL-3.0, see [LICENSE](LICENSE). This started as the [Discord Rich Presence plugin](https://github.com/navidrome/discord-rich-presence-plugin) by the Navidrome team. In 2026 the Discord login with user tokens was replaced by signed events to the bridge, and the Deezer/iTunes cover lookup was added.
