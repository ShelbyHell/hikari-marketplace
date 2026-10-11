# Tools

Addons that are not payment methods: bots, admin pages and the rest. They speak protocol 2,
[PROTOCOL-v2.md](../PROTOCOL-v2.md), never the payment protocol (1), so older panels never
offer one. Each goes in `tools/<id>/`: `adapter.json` with `"category": "tools"`,
`"protocol": 2` and its `kind`, `mode`, `permissions` and `resources`, a `Dockerfile`, and the
code with its tests when the code lives here. It is published as `ghcr.io/getmikan/tool-<id>`.

| id | |
|---|---|
| [`telegram`](telegram) | the official mikan bot, built from mikan's own source at the release its version names |

CI builds every image with the build argument `VERSION`, the tool's version. The release
plan rebuilds a tool when its directory changes (with a new version), and when the shared
code of this repository changes only if its Dockerfile copies that code.
