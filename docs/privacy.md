# Privacy notes for whoever runs the bridge

If you run this for other people, you're handling their data. This page lists what's stored, where it goes and which settings matter. It's practical advice, not legal advice. Once people outside your own household use it, privacy law like the UK or EU GDPR treats you as the one responsible.

The bridge already shows a privacy notice at `/privacy`, and the link button points to it. Set `NDS_PRIVACY_CONTACT` so people know how to reach you.

## What's stored and for how long

- **The link itself:** Navidrome username, Discord user ID and name, when it was linked and last used. It's kept in `links.json` until the user unlinks, or until it hasn't been used for `NDS_LINK_RETENTION_DAYS` (365 by default).
- **The Discord refresh token:** in `links.json` too, encrypted. It's revoked at Discord when someone unlinks or the link gets removed.
- **The Discord access token:** only in memory, for about a week.
- **The browser session on the link page:** only in memory, for 12 hours at most.
- **What's playing right now:** only in memory while it's shown.
- **Logs:** sign-ins and link changes with time, IP address and username. They're kept as long as journald keeps them. The example config in `bridge/deploy/journald-retention.conf` keeps them for 14 days. Set `NDS_LOG_RETENTION_DAYS` to the same number so the privacy notice is right.

Discord user IDs only show up in the logs at debug level. Navidrome passwords are passed to Navidrome once at sign-in and never stored or logged.

## Who else gets something

- **Discord** (USA) gets the status, meaning track, artist, album and cover link, for linked users while they play.
- **The Cover Art Archive** (MetaBrainz, USA) gets MusicBrainz release IDs, if that source is turned on in the plugin.
- **Deezer** (France) and **Apple iTunes** (USA) get artist and album names, if that source is turned on.
- **ListenBrainz** (MetaBrainz, USA) gets artist, title, album and recording ID, if Spotify links are turned on.
- **uguu.se** gets the cover image itself, and it's public there for about 3 hours. That only happens if you turned on the upload and no other source had the cover.

All these lookups come from your server's IP and don't include anyone's name. None of them happen for users who haven't linked.

## What users can do

- Linking is opt-in. Unlinking on the page, or removing the app in Discord under Authorized Apps, takes the consent back.
- Unlinking deletes their record right away and revokes the token. You can also remove someone yourself with `nds-bridge-admin links remove <user>` (stop the service first).
- The start page shows what's stored about them. `nds-bridge-admin links list` shows all links.

## Settings worth thinking about

- A shorter `NDS_LINK_RETENTION_DAYS` means less data kept around.
- Turn off the uguu.se upload in the plugin if you don't want covers on a public host.
- In Navidrome's plugin settings, only allow the users who actually want this.
