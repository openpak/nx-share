# Deploy

Quadlet: `deploy/openpak-nx-share.container`. Traefik: `deploy/traefik-nx-share.yml`
(TCP passthrough by SNI; this service terminates TLS with an OpenPak-CA leaf).

| Variable | Default | What |
| --- | --- | --- |
| `NX_ADDR` | `:21000` | Console listener (TLS) |
| `NX_CA_DIR` | `/ca` | OpenPak CA (`ca.pem`, `ca.key`) for the leaf |
| `NX_CORE_URL` | `http://openpak-nx-baas:20070` | nx-baas game API (`/internal/switch/identity`) |
| `NX_INTERNAL_KEY` | (shared `.env`) | Internal key for nx-baas |
| `NX_VERSE_GRPC` | `openpak-nn-juxtaposition-api:20091` | OpenVerse internal gRPC |
| `NX_VERSE_API_KEY` | `PN_MIIVERSE_API_GRPC_SERVER_API_KEY` | OpenVerse gRPC API key (read from the shared `.env` when unset) |
| `NX_VERSE_URL` | `https://verse.openpak.org` | Public address used in the post link |
| `NX_TITLES_URL` | `http://openpak-website:20010/api/v1/titles` | Title list for game names |
| `NX_PARENTAL_ADDR`, `NX_PARENTAL_KEY` | unset | Parental controls core gRPC; unset = no server-side supervision check |
| `NX_SHARE_MAX_PER_HOUR` | `10` | Posts per account per hour |
| `NX_DEBUG` | unset | Log every TLS ClientHello |

The console's DNS already sends `*.nintendo.net` to OpenPak, so no ceiling change is needed:
without the Traefik router the host lands on nx-baas's catch-all.
