package main

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

func TestBuildRSSFeed(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	cfg := feedConfig{
		Enabled:                true,
		Format:                 "rss",
		FeedPath:               "/rss",
		Language:               "en-us",
		MaxItems:               25,
		DescriptionSource:      "intro_body",
		DescriptionLimit:       80,
		IncludeContentEncoded:  true,
		IncludeImageEnclosures: true,
	}
	items := []itemRow{{
		Title:        "Hello Feed",
		Slug:         "hello-feed",
		Intro:        "<p>A short intro.</p>",
		Body:         "<p>The full body.</p>",
		Image:        "/media/hello.png",
		CreatedAt:    now,
		UpdatedAt:    now,
		CategoryName: "News",
	}}

	raw, contentType, err := buildFeed("Example", "https://example.test", cfg, items)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "application/rss+xml; charset=utf-8" {
		t.Fatalf("content type = %q", contentType)
	}
	if err := xml.Unmarshal(raw, new(any)); err != nil {
		t.Fatalf("invalid XML: %v\n%s", err, raw)
	}
	text := string(raw)
	for _, want := range []string{
		"<rss",
		"<title>Hello Feed</title>",
		"<content:encoded>",
		`url="https://example.test/media/hello.png"`,
		"https://example.test/content/item/hello-feed",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("RSS missing %q:\n%s", want, text)
		}
	}
}

func TestBuildAtomFeed(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	cfg := feedConfig{
		Enabled:           true,
		Format:            "atom",
		FeedPath:          "/rss",
		MaxItems:          25,
		DescriptionSource: "intro_body",
		DescriptionLimit:  80,
	}
	items := []itemRow{{
		Title:     "Hello Atom",
		Slug:      "hello-atom",
		Intro:     "A short intro.",
		CreatedAt: now,
		UpdatedAt: now,
	}}

	raw, contentType, err := buildFeed("Example", "https://example.test", cfg, items)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "application/atom+xml; charset=utf-8" {
		t.Fatalf("content type = %q", contentType)
	}
	if err := xml.Unmarshal(raw, new(any)); err != nil {
		t.Fatalf("invalid XML: %v\n%s", err, raw)
	}
	text := string(raw)
	for _, want := range []string{
		`<feed xmlns="http://www.w3.org/2005/Atom">`,
		"<title>Hello Atom</title>",
		`href="https://example.test/rss" rel="self" type="application/atom+xml"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Atom missing %q:\n%s", want, text)
		}
	}
}

func TestConfigFromMapCategories(t *testing.T) {
	cfg := configFromMap(map[string]any{
		"category_ids":  "3, 2 3",
		"category_1_id": float64(9),
		"category_2_id": "2",
		"max_items":     float64(999),
	})
	want := []uint{2, 3, 9}
	if len(cfg.CategoryIDs) != len(want) {
		t.Fatalf("category ids = %#v", cfg.CategoryIDs)
	}
	for i := range want {
		if cfg.CategoryIDs[i] != want[i] {
			t.Fatalf("category ids = %#v, want %#v", cfg.CategoryIDs, want)
		}
	}
	if cfg.MaxItems != 500 {
		t.Fatalf("MaxItems = %d", cfg.MaxItems)
	}
}
