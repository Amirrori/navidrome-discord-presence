# Setup

This walks through installing the bridge on a Debian machine (a VM or container is fine) and hooking up Navidrome. The bridge serves plain HTTP. Adding HTTPS with your own reverse proxy is optional and covered at the end.

The examples use made-up addresses, swap in your own:

- `192.0.2.17` is the bridge
- `192.0.2.10` is Navidrome (port 4533)
- `192.0.2.0/24` is the network your users are on

Plan for about an hour. The first two steps happen in the Discord developer portal and you have to click through them yourself.

## 1. Create the Discord application

1. Create a team in the [Discord developer portal](https://discord.com/developers/teams) if you don't have one yet. The Social SDK only works for applications that belong to a team, a team with just you in it is fine.
2. [Create an application](https://discord.com/developers/applications) and pick that team as its owner. Its name is what people will see after "Listening to", so something like "Navidrome" works well.
3. In the sidebar under Discord Social SDK, open Getting Started, fill in the short form and submit it.
4. On the OAuth2 page, add the redirect `http://192.0.2.17:8091/callback`, make sure Public Client is off, and copy the client secret (reset it if you don't have it anymore).
5. On General Information, copy the Application ID.

The redirect has to match the link page address exactly, `NDS_PUBLIC_URL` plus `/callback`, including http or https and the port.

Turn on two-factor login for everyone in that team. Whoever controls the team can change the redirect.

## 2. Download the Social SDK

In the sidebar under Discord Social SDK, open Downloads, pick the newest version and download the C++ SDK. You only need `include/` and `lib/release/libdiscord_partner_sdk.so` out of the zip.

## 3. Build

You need Go 1.26.10 or newer, CMake and a C++20 compiler. Build the worker on the same distribution it'll run on (or one with an older glibc), otherwise it won't start.

```sh
# bridge, a static binary that runs anywhere
cd bridge/server

CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o nds-bridge ./cmd/nds-bridge

# worker
cd ../worker
cmake -S . -B build -DDISCORD_SOCIAL_SDK_DIR=/path/to/discord_social_sdk
cmake --build build
# you get build/nds-worker and build/libdiscord_partner_sdk.so
```

On Debian the worker needs `g++ make cmake libpulse-dev libasound2-dev libx11-dev` to build and `libpulse0 libasound2t64 libx11-6 libatomic1` to run. Compiling the SDK header takes about 2 GB of RAM, so give a small container more memory while building.

Build the plugin so it can only reach your bridge and nothing else on your network (more in [plugin/README.md](../plugin/README.md)). This needs TinyGo, see the plugin README:

```sh
cd ../../plugin
make package BRIDGE_HOSTS=192.0.2.17
```

## 4. Install the bridge

As root on the bridge machine, in a copy of this repository with the files you built in step 3:

```sh
apt install libpulse0 libasound2t64 libx11-6 libatomic1 nftables

useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin nds-bridge

install -m 755 bridge/server/nds-bridge /usr/local/bin/nds-bridge
install -d -m 755 /opt/navidrome-discord-status
install -m 755 bridge/worker/build/nds-worker bridge/worker/build/libdiscord_partner_sdk.so /opt/navidrome-discord-status/
install -m 755 bridge/deploy/nds-bridge-admin /usr/local/sbin/nds-bridge-admin

install -d -m 755 /etc/navidrome-discord-status
install -d -m 700 /etc/navidrome-discord-status/credentials
install -m 644 bridge/deploy/bridge.env.example /etc/navidrome-discord-status/bridge.env

# the secrets stay readable by root only, systemd hands them to the service
umask 077
head -c 48 /dev/urandom | base64 -w0 > /etc/navidrome-discord-status/credentials/event-secret   # the bridge secret, the plugin needs it too
nano /etc/navidrome-discord-status/credentials/discord-client-secret   # paste the client secret
```

Then fill in `/etc/navidrome-discord-status/bridge.env`. Every setting is explained in [bridge/README.md](../bridge/README.md).

The bridge logs sign-ins with IP addresses, so keep the logs short: copy `bridge/deploy/journald-retention.conf` to `/etc/systemd/journald.conf.d/retention.conf` (create the folder if it doesn't exist) and restart `systemd-journald`.

## 5. Firewall

Copy `bridge/deploy/nftables.conf.example` to `/etc/nftables.conf` and set the addresses at the top: `EVENTS_FROM` is Navidrome, `WEB_FROM` is whoever opens the link page, `LOGIN_CHECK` and `LOGIN_CHECK_PORT` are where `NDS_NAVIDROME_URL` points. If Navidrome runs on the same machine, also remove the `# Navidrome: ` prefix from the three lines marked that way. Then load it:

```sh
nft -c -f /etc/nftables.conf      # check it first, the nds-bridge user has to exist
systemctl enable nftables
systemctl restart nftables        # restart, not start: a running nftables keeps its old rules
```

After that Navidrome can send events, your users can open the link page, and the bridge itself can only reach DNS, Navidrome and the internet on port 443.

## 6. Start it

```sh
install -m 644 bridge/deploy/nds-bridge.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now nds-bridge
journalctl -u nds-bridge -f
```

With plain http you'll see two warnings in the log, one for the link page and one for Navidrome. They just tell you that sign-ins aren't encrypted. `systemd-analyze security nds-bridge` should come out low.

## 7. Connect Navidrome

1. Copy `discord-status.ndp` into Navidrome's plugins folder. If `Plugins.AutoReload` is off, restart Navidrome.
2. In Navidrome go to Settings, Plugins, Discord Status. Enable it, pick which users may use it and fill in:
   - Bridge URL: `http://192.0.2.17:8090`
   - Bridge secret: the event secret from step 4. Show it with `cat /etc/navidrome-discord-status/credentials/event-secret` (as root on the bridge machine) and copy the whole line.
   - the cover sources and Spotify links, however you like
   Only the users you pick here get a status. Anyone else, an admin account included, can play music as usual and nothing is sent.
3. Users open `http://192.0.2.17:8091`, sign in with their Navidrome login and click "I agree, link my Discord account".

When you replace the `.ndp` file later, Navidrome turns the plugin off to be safe. Just enable it again.

## Checking that it works

- Play something. The status should show up within a few seconds.
- `http://192.0.2.17:8091/privacy` should show the privacy notice.

If you want to test without Navidrome, `nds-test-event` (in `bridge/server/cmd`) sends a signed event the same way the plugin does.

## When something's wrong

**Plugin log says `host "..." is not allowed`.** The bridge's address isn't in the plugin's allowed hosts. Rebuild the plugin with `BRIDGE_HOSTS` set to the host name and IP you use in the Bridge URL.

**Plugin log shows `connection refused` or timeouts.** Either the bridge isn't running, or the firewall doesn't let Navidrome in (`EVENTS_FROM`).

**The link page says "Discord refused the request (invalid_scope)".** The Social SDK isn't enabled for the application yet. Check that the application belongs to a team and that you submitted the form under Discord Social SDK, Getting Started (step 1).

**Discord says "Invalid OAuth2 redirect_uri".** The redirect in the developer portal doesn't exactly match `NDS_PUBLIC_URL` plus `/callback`. Check http vs https, the port and trailing slashes.

**systemd says `status=203/EXEC` even though the binary is there.** The binary was built dynamically on another system (NixOS does that, for example). Build the bridge with `CGO_ENABLED=0`.

**The status shows but without a cover.** None of the cover sources had it. Turn on the uguu.se upload, or check `NDS_IMAGE_HOSTS`.

**Changes show up a few seconds late.** That's on purpose. Updates are at least 5 seconds apart so Discord doesn't rate limit you.

## Updating

Swap `/usr/local/bin/nds-bridge` (or the worker files) and restart the service. Nobody has to link again. For the plugin, replace the `.ndp` and turn it back on in Navidrome.

## Removing it

Ask users to unlink on the page (or in Discord under Authorized Apps). Then turn off the plugin, stop and disable `nds-bridge` and delete `/var/lib/navidrome-discord-status`, `/etc/navidrome-discord-status` and the binaries.

## Adding HTTPS (optional)

The bridge doesn't do HTTPS itself. If you want it, put your own reverse proxy in front, for example Caddy, nginx or Traefik. `bridge/deploy/Caddyfile.example` is a starting point for Caddy. In short:

1. Give the link page a host name with a certificate, e.g. `presence.example.com`, and send everything there to the bridge's port 8091.
2. Send `/v1/` to port 8090, and only allow that from your Navidrome server.
3. In `bridge.env` set `NDS_PUBLIC_URL=https://presence.example.com` and `NDS_TRUSTED_PROXIES` to the proxy's address. If Navidrome has an https address, use that for `NDS_NAVIDROME_URL` too.
4. In the Discord developer portal, change the redirect to `https://presence.example.com/callback`.
5. In the firewall set `EVENTS_FROM` and `WEB_FROM` to the proxy, and `LOGIN_CHECK` to wherever `NDS_NAVIDROME_URL` now points.
6. Rebuild the plugin with `BRIDGE_HOSTS=presence.example.com,<proxy IP>` and set its Bridge URL to `https://presence.example.com`.
