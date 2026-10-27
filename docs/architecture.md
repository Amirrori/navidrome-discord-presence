# How it works

```
browser           -->  nds-bridge :8091   link page and Discord login
navidrome plugin  -->  nds-bridge :8090   /v1/events and /v1/linked

nds-bridge keeps links.json (refresh tokens encrypted with AES-256-GCM)
nds-bridge starts one nds-worker per active user, and the worker talks to Discord
```

The bridge serves plain HTTP. It has no HTTPS of its own; if you want it, put your own reverse proxy in front (see the end of [setup.md](setup.md)).

## The pieces

The **plugin** is Go compiled to WebAssembly with TinyGo, and it runs inside Navidrome's plugin sandbox. It gets Navidrome's playback reports, asks the bridge whether the user has linked Discord, finds a cover and links, and sends a signed event. It never sees anything from Discord.

**nds-bridge** is Go and only uses the standard library. It serves the link page, checks Navidrome logins once when someone signs in, does the Discord OAuth flow with the client secret, keeps the refresh tokens encrypted, renews access tokens and looks after the workers.

**nds-worker** is a small C++ program around Discord's closed-source Social SDK. Every user gets their own process, so if the SDK crashes or misbehaves it only affects that one user. It talks to the bridge over stdin and stdout.

## Linking an account

1. The user signs in with their Navidrome username and password. The bridge passes them once to Navidrome's `/auth/login` and only remembers the username in a server-side session.
2. Clicking "link" creates a random OAuth `state` (tied to that session, usable once, valid for 10 minutes) and a PKCE verifier, and sends the user to Discord.
3. Discord sends them back to `/callback`. The bridge checks the state, swaps the code for tokens using the client secret, looks up the Discord user ID and stores the refresh token encrypted, tied to the Navidrome username.
4. Unlinking deletes the record and revokes the token at Discord.

## Plugin to bridge

Both endpoints take a POST with a JSON body of at most 8 KB and answer with `{"linked": true}` or `{"linked": false}`.

`/v1/linked` takes `{"v":1,"user":"alice"}` and just answers whether that user has linked.

`/v1/events` takes `{"v":1,"user":"alice","state":"playing","track":{...}}`. The state is `playing`, `paused` or `stopped`. The track has title, artist, album, start and end time in milliseconds, and optional cover, track and artist links.

Every request has three headers:

```
X-NDS-Timestamp: unix time in seconds
X-NDS-Nonce:     32 random hex characters
X-NDS-Signature: v1=<hex HMAC-SHA256 of "timestamp.nonce.body">
```

The bridge throws out anything older or newer than 30 seconds, anything with a nonce it has already seen and anything with a bad signature. It does that before it even parses the JSON, and it rejects unknown fields. The plugin remembers each user's answer for a while (10 minutes if linked, 2 minutes if not) and does nothing at all for users who haven't linked.

## Bridge to worker

They send one JSON object per line. The bridge sends commands on stdin:

```
{"op":"connect","token":"..."}
{"op":"token","token":"..."}
{"op":"activity","activity":{...}}
{"op":"clear"}
{"op":"shutdown"}
```

and the worker answers on stdout with `ready`, `disconnected`, `error` or `log` events. The access token only ever goes over stdin, never as an argument or environment variable. When stdin closes the worker clears the status and exits, so it can't outlive the bridge.

## Timing

- Status updates are at least 5 seconds apart. That keeps us inside Discord's rate limit, and if you skip through a few songs quickly only the last one gets sent.
- If a song should have ended 60 seconds ago and nothing new came in, the status is cleared. Same for a pause that lasts an hour, or a song with no known length after 30 minutes. That way a lost "stopped" (Navidrome restarting, an app being closed) can't leave a status behind.
- After you stop, the worker stays connected for 10 minutes so the next song shows up right away. Then it disconnects.
- Access tokens last about a week and get renewed a day before they run out.
- Links nobody used for a year are removed (you can change that).

## Security

- Secrets are passed in as files (systemd credentials), never as values.
- Refresh tokens are encrypted on disk with a key made on first start, and each one is tied to its username so it can't be moved to another account.
- Status text gets cleaned of control characters and invisible formatting and cut to Discord's length limits. Images and links have to be https and on an allowlist.
- The link page uses server-side sessions, `__Host-` cookies, CSRF tokens, login rate limits, a strict Content Security Policy with no scripts at all, and a new session ID after signing in.
- The systemd unit and the example firewall lock the bridge and its workers down: no privileges, a read-only system, and no way to reach private addresses except Navidrome for the login check.

More in [SECURITY.md](../SECURITY.md).
