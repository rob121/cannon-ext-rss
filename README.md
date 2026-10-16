# cannon-ext-rss

`cannon-ext-rss` is a Cannon extension that:

- publishes selected content categories as a top-level RSS or Atom feed route, and
- embeds remote RSS/Atom feeds in template spaces via an **RSS Feed** block.

On install it:

- migrates the extension configuration table,
- migrates the remote feed cache table (`ext_rss_feed_cache`),
- registers an endpoint named `feed`,
- registers the `rss-feed` block,
- creates `/rss` as an active `Extension Endpoint` route when no route already owns that path,
- attaches the route to the `public` frontend group when available.

## Outbound feed configuration

Open the extension settings in Cannon and choose up to ten category dropdowns, or enter additional category IDs in the comma-separated field. Useful feed settings include:

- RSS or Atom output,
- feed title, description, language, copyright, editor, webmaster, and TTL,
- canonical site URL and feed path,
- maximum item count,
- optional descendant category inclusion,
- public visibility filtering,
- description source and length,
- optional `content:encoded` entries,
- optional image enclosures,
- **remote feed cache minutes** (default lifetime for cached remote feed URLs used by the block).

If no categories are selected, the feed emits the latest public published content from all categories.

## RSS Feed block

Place the **RSS Feed** block in a template space and configure:

| Field | Purpose |
| --- | --- |
| Feed URL | Full `http(s)` URL to an RSS or Atom feed |
| Number of items | How many entries to render (default 5, max 100) |
| Cache lifetime (minutes) | Per-placement override for how long the fetched feed body is reused |
| Heading | Optional heading; defaults to the remote feed title |
| Show descriptions | Include a short plain-text summary under each item |

Cached bodies are stored per feed URL in `ext_rss_feed_cache`. When the lifetime expires the extension refetches; if a refresh fails, the last good cached body is still served.

## Build

```sh
go build -o cannon-ext-rss .
```
