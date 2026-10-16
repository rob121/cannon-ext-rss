package main

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/rob121/cannon/extension"
)

const (
	blockID         = "rss-feed"
	defaultMaxItems = 5
)

func (app *rssApp) blockDefinitions() ([]extension.BlockDefinition, error) {
	return []extension.BlockDefinition{app.blockDefinition()}, nil
}

func (app *rssApp) blockDefinition() extension.BlockDefinition {
	return extension.BlockDefinition{
		ID:     blockID,
		Title:  "RSS Feed",
		Spaces: []string{"sidebar", "above-main", "below-main", "above-footer", "home1", "home2"},
		Fields: []extension.BlockField{
			{
				Name:     "feed_url",
				Label:    "Feed URL",
				Type:     "text",
				Required: true,
				Help:     "Full http(s) URL to an RSS or Atom feed.",
			},
			{
				Name:  "max_items",
				Label: "Number of items",
				Type:  "text",
				Help:  "How many entries to show. Defaults to 5 (max 100).",
			},
			{
				Name:  "cache_minutes",
				Label: "Cache lifetime (minutes)",
				Type:  "text",
				Help:  "How long to reuse a cached copy of this feed URL. Leave blank to use the extension default.",
			},
			{
				Name:  "title",
				Label: "Heading",
				Type:  "text",
				Help:  "Optional heading. Defaults to the feed title when available.",
			},
			{
				Name:  "show_description",
				Label: "Show descriptions",
				Type:  "select",
				Help:  "Include a short description under each item.",
				Options: []extension.BlockFieldOption{
					{Value: "1", Label: "Yes"},
					{Value: "0", Label: "No"},
				},
			},
		},
	}
}

func (app *rssApp) block(item string, req extension.WireRequest) extension.WireResponse {
	feedURL := strings.TrimSpace(extension.BlockDataString(req, "feed_url"))
	if feedURL == "" {
		return extension.HTML(http.StatusOK, "")
	}

	maxItems := defaultMaxItems
	if n, ok := extension.BlockDataInt(req, "max_items"); ok && n > 0 {
		maxItems = n
	}

	cacheMinutes := app.defaultRemoteCacheMinutes()
	if n, ok := extension.BlockDataInt(req, "cache_minutes"); ok && n > 0 {
		cacheMinutes = n
	}

	showDescription := true
	if raw := strings.TrimSpace(extension.BlockDataString(req, "show_description")); raw != "" {
		showDescription = raw == "1" || strings.EqualFold(raw, "yes") || strings.EqualFold(raw, "true")
	}

	items, feedTitle, err := loadRemoteItems(app.db, feedURL, maxItems, cacheMinutes)
	if err != nil {
		return extension.HTML(http.StatusOK, "<!-- rss feed error: "+template.HTMLEscapeString(err.Error())+" -->")
	}
	if len(items) == 0 {
		return extension.HTML(http.StatusOK, "")
	}

	title := strings.TrimSpace(extension.BlockDataString(req, "title"))
	if title == "" {
		title = feedTitle
	}

	return app.render("rss/block.html", map[string]any{
		"Title":           title,
		"Items":           items,
		"ShowDescription": showDescription,
		"FeedURL":         feedURL,
		"Space":           extension.BlockSpace(req),
		"BlockItem":       item,
		"CacheMinutes":    normalizeCacheMinutes(cacheMinutes),
	})
}

func (app *rssApp) defaultRemoteCacheMinutes() int {
	cfg, err := loadConfig(app.store)
	if err != nil {
		return defaultCacheMinutes
	}
	return normalizeCacheMinutes(cfg.RemoteCacheMinutes)
}

func (app *rssApp) render(name string, data map[string]any) extension.WireResponse {
	body, err := app.templates.Execute(name, data)
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	return extension.HTML(http.StatusOK, body)
}
