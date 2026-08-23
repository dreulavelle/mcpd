# PulseMCP sub-registry fixtures

`list-example.json` and `detail-example.json` are PulseMCP's own published
example payloads for the v0.1 sub-registry API, extracted verbatim from
<https://www.pulsemcp.com/api/docs/v0.1> on 2026-08-23.

They are the vendor's bytes rather than captured traffic, and that is a
deliberate second choice. v0.1 authenticates every request with `X-API-Key` and
`X-Tenant-ID`, and PulseMCP issues those by email rather than self-service, so
there is no key here to capture a real response with. The published examples
are the closest thing to the real shape that can be obtained without one, and
they are copied rather than retyped so that nothing in this directory is
written from a prose description of the format.

What makes that acceptable rather than merely unavoidable: v0.1 implements the
**Generic MCP Registry API**, the same wire contract the official registry
serves and `generic.go` already reads against real captured responses in
`official_test.go`. The parts that are specific to PulseMCP are the base URL,
the two credential headers, and the `_meta` key — and those are exactly what
`pulsemcp_test.go` exercises directly rather than through a fixture.

## Why v0.1 and not v0beta

The obvious integration is the unauthenticated `v0beta` API
(`GET /v0beta/servers?count_per_page=&offset=`, with a `remotes[]` array
carrying `url_direct`, `url_setup`, `transport`, `authentication_method` and
`cost`). It is being switched off, and not gradually enough to build on.

PulseMCP fails a rising share of `v0beta` requests on purpose, and says so in
the body of the ones it fails:

```
410 {"error":{"code":"API_SUNSET","message":"The v0beta API is deprecated and
being sunset. This request was randomly failed as part of the sunset process.
Starting January 2026: 1% of requests fail. Starting April 2026: 10%. Starting
June 2026: 50%. September 2026: Fully sunset (100%). Please migrate to our
v0.1 API at /api."}}
```

Measured 2026-08-23: three of six consecutive requests returned `410`. Full
sunset is a week away.

Two fields that only `v0beta` had are therefore gone, and no substitute is
invented for them:

- **`cost`** (`free_tier`, and so on) has no equivalent in v0.1 at all. No
  entry field is carried for it, because a field no source can populate is a
  column of blanks.
- **`authentication_method`** becomes `_meta["com.pulsemcp/server-version"]
  .remotes[N].authOptions[].type`, which PulseMCP documents as a **premium**
  enrichment — absent for an ordinary tenant. `Entry.Auth` is therefore derived
  from the composed document by `describe()`, uniformly for all four sources,
  rather than read from a field that would be empty on most rows and mean
  something different on each source that had it.

## Terms

`pulsemcp.com/robots.txt` publishes `User-agent: *`, `Allow: /`, and
`Content-Signal: search=yes,ai-train=no,use=reference` — the same declaration
Smithery makes. `search=yes` is that signal's own wording for "building a
search index and providing search results", which is what this source does and
the whole of it.
