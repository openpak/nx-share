# Protocol

How the Switch's Album posts a capture, and what this service answers. Firmware 23.0.1.

## The share applet

- Album (photoViewer) starts the LoginShare library applet. On firmware 23 that applet id runs
  the system web program (`0100000000001042`, systemWeb) in its Share usage; the old
  `0100000000001010` program is a 30 KB stub.
- systemWeb opens `https://web-<env>.share.srv.nintendo.net/` (`/settings/` for the settings
  page), `<env>` = `lp1` on retail. The URL carries the query parameters `app_id` (the capture's
  application id, 16 hex digits), `file_count` and `file_type` (`image` or `video`). Nintendo's
  own server answers this page only when `app_id` is present.
- The page is given the selected captures through
  `window.nx.system.setUploadFile(<input type=file>)`: the input's `change` event delivers the
  files (up to 4 JPEG screenshots, or an MP4 with its JPEG thumbnail as the second file).
- The console user is proven with
  `window.nx.system.setNsaIdTokenAvailableCallback(function(error, idToken){...})`: the network
  service account's id token, which OpenPak's nx-baas signs.
- The page ends the applet by navigating to the callback URL
  `https://localhost/share/result=success&service=<name>&id=<post id>`
  (or `result=cancel`, `result=error`). The service name and post id are handed back to the
  caller.
- The console's footer buttons are set with `window.nx.footer.setAssign(button, label, fn)`.

## What this service does

1. `GET /` serves the post page. It logs the query parameter names and the three known values.
2. The page sends the NSA id token to `POST /_openpak/whoami`. nx-baas proves it
   (`POST /internal/switch/identity`, internal key): a banned account is refused, a token with
   no OpenPak account behind it is refused, and a supervised account (parental controls core,
   when configured) is refused.
3. The page reads the first screenshot with `FileReader.readAsDataURL` and shows it.
4. `POST /_openpak/post` `{id_token, app_id, message, image, spoiler}`: the checks of step 2
   again, a 280-character message, a 16:9 JPEG between 800 and 1920 pixels wide, and at most 10
   posts per account per hour. The screenshot is re-encoded at 800x450 (OpenVerse's 16:9 size;
   this also drops the capture's EXIF).
5. OpenVerse, over its internal gRPC channel: `POST /api/v1/sso/resolve` (the OpenVerse user of
   the OpenPak account, never renamed by a share), then `POST /api/v1/console-share/posts` as
   that user. OpenVerse applies bans, read-only accounts, community permissions, automod and its
   duplicate check. The post goes to the community that has the game's Switch title id, or to
   the Switch room, with the game's name as its topic tag.
6. The page shows the post's address and closes with `service=openverse` and the post id.

## Refusals the page shows

`banned`, `supervised`, `not_linked`, `unknown_console`, `rules` (automod), `too_many_posts`,
`bad_image`, `not_allowed` (community permissions), `verse_unavailable`, and `video` (decided
on the page).
