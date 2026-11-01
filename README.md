# Switch Album Share Server

OpenPak's server for the Switch Album's **Post** button: screenshots go to OpenVerse.

| | |
| --- | --- |
| Network | Switch |
| Role | The share applet's page: posts an Album screenshot to OpenVerse as the console's user |
| Depends on | [`nx-baas`](https://github.com/openpak/nx-baas) (who the console user is), [`nn-juxtaposition`](https://github.com/openpak/nn-juxtaposition) (OpenVerse), the website's title list |
| Image | `ghcr.io/openpak/nx-share` |
| Licence | AGPL-3.0-only |

## What it serves

| Host | What |
| --- | --- |
| `web-<env>.share.srv.nintendo.net` `/` | The post page the share applet opens (Album -> Sharing and Editing -> Post) |
| `/settings/` | The posting settings page System Settings opens: there is nothing to set up |
| `POST /_openpak/whoami` | The console user behind the page's NSA id token, and the game's name |
| `POST /_openpak/post` | Scales the screenshot to 800x450 and creates the OpenVerse post |

Videos are not posted (OpenVerse has no video posts); the page says so.

## Run

Runs as a container from `deploy/openpak-nx-share.container`, behind Traefik's TLS
passthrough (`deploy/traefik-nx-share.yml`). Environment variables are in
[deploy.md](docs/deploy.md).

Tests:

    GOWORK=off go vet ./... && GOWORK=off go test ./...

## Docs

- [Protocol](docs/protocol.md)
- [Deploy](docs/deploy.md)

## Licence

AGPL-3.0-only. See [LICENSE](LICENSE).
