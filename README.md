# cannon-ext-rss

`cannon-ext-rss` is a Cannon extension that publishes selected content categories as a top-level feed route.

On install it:

- migrates the extension configuration table,
- registers an endpoint named `feed`,
- creates `/rss` as an active `Extension Endpoint` route when no route already owns that path,
- attaches the route to the `public` frontend group when available.

## Configuration

Open the extension settings in Cannon and choose up to ten category dropdowns, or enter additional category IDs in the comma-separated field. Useful feed settings include:

- RSS or Atom output,
- feed title, description, language, copyright, editor, webmaster, and TTL,
- canonical site URL and feed path,
- maximum item count,
- optional descendant category inclusion,
- public visibility filtering,
- description source and length,
- optional `content:encoded` entries,
- optional image enclosures.

If no categories are selected, the feed emits the latest public published content from all categories.

## Build

```sh
go build -o cannon-ext-rss .
```
