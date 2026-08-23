# Docker catalogue fixtures

`catalog-excerpt.yaml` is a verbatim excerpt of Docker's built MCP catalogue,
fetched from <https://desktop.docker.com/mcp/catalog/v3/catalog.yaml> on
2026-08-22. The header and seven whole entries are copied byte for byte; the
other three hundred and ten entries are removed and nothing else is changed.

The seven were chosen to cover every branch the translation has:

| entry | why it is here |
|---|---|
| `context7` | `type: remote`, streamable-http, a custom header carrying `${CONTEXT7_API_KEY}` |
| `apify` | `type: remote`, streamable-http, `Authorization: Bearer ${APIFY_API_KEY}` — and the same endpoint as `com.apify/apify-mcp-server` in the official registry, which is the cross-source duplicate |
| `astro-docs` | `type: remote`, streamable-http, no credential at all |
| `dodo-payments` | `type: remote` but `sse`, which this host does not connect over |
| `linear` | `type: remote`, streamable-http, reachable only through an OAuth flow Docker's gateway performs |
| `SQLite` | `type: server` — a container Docker runs locally |
| `curl` | `type: poci` — a command Docker runs locally |

The catalogue is built from
[docker/mcp-registry](https://github.com/docker/mcp-registry), which is MIT
licensed. `LICENSE` in this directory is that project's licence file, fetched
from the repository and reproduced unchanged, as the licence requires.
