package main

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestParseRemoteRSSFeed(t *testing.T) {
	body := `<?xml version="1.0"?>
<rss version="2.0">
  <channel>
    <title>Camp News</title>
    <item>
      <title>First</title>
      <link>https://example.test/first</link>
      <description><![CDATA[<p>Hello world</p>]]></description>
      <pubDate>Thu, 15 Oct 2026 12:00:00 +0000</pubDate>
    </item>
    <item>
      <title>Second</title>
      <link>https://example.test/second</link>
      <description>More news</description>
    </item>
  </channel>
</rss>`
	items, title, err := parseRemoteFeed(body)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Camp News" {
		t.Fatalf("title = %q", title)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	if items[0].Title != "First" || items[0].Link != "https://example.test/first" {
		t.Fatalf("first item = %#v", items[0])
	}
	if items[0].Description != "Hello world" {
		t.Fatalf("description = %q", items[0].Description)
	}
}

func TestParseRemoteAtomFeed(t *testing.T) {
	body := `<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom News</title>
  <entry>
    <title>Atom One</title>
    <link href="https://example.test/atom-one" rel="alternate"/>
    <updated>2026-10-15T12:00:00Z</updated>
    <summary>Summary text</summary>
  </entry>
</feed>`
	items, title, err := parseRemoteFeed(body)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Atom News" || len(items) != 1 {
		t.Fatalf("title=%q items=%d", title, len(items))
	}
	if items[0].Link != "https://example.test/atom-one" {
		t.Fatalf("link = %q", items[0].Link)
	}
}

func TestCachedFeedBodyRespectsLifetime(t *testing.T) {
	db := openTestDB(t)
	feedURL := "https://example.test/feed.xml"
	calls := 0
	original := fetchFeedBody
	fetchFeedBody = func(url string) ([]byte, error) {
		calls++
		return []byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><item><title>A</title><link>https://example.test/a</link></item></channel></rss>`), nil
	}
	t.Cleanup(func() { fetchFeedBody = original })

	items, _, err := loadRemoteItems(db, feedURL, 5, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || calls != 1 {
		t.Fatalf("first load items=%d calls=%d", len(items), calls)
	}

	items, _, err = loadRemoteItems(db, feedURL, 5, 30)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected cache hit, calls=%d", calls)
	}

	// Expire the cache row and ensure a refresh happens.
	if err := db.Table(cacheTable).Where("feed_url = ?", feedURL).Updates(map[string]any{
		"fetched_at": time.Now().Add(-2 * time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}
	_, _, err = loadRemoteItems(db, feedURL, 5, 30)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expected refresh after expiry, calls=%d", calls)
	}
}

func TestCachedFeedBodyServesStaleOnFetchError(t *testing.T) {
	db := openTestDB(t)
	feedURL := "https://example.test/stale.xml"
	original := fetchFeedBody
	fetchFeedBody = func(url string) ([]byte, error) {
		return []byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><item><title>Cached</title><link>https://example.test/c</link></item></channel></rss>`), nil
	}
	if _, _, err := loadRemoteItems(db, feedURL, 5, 1); err != nil {
		t.Fatal(err)
	}
	fetchFeedBody = func(url string) ([]byte, error) {
		return nil, errors.New("network down")
	}
	t.Cleanup(func() { fetchFeedBody = original })

	if err := db.Table(cacheTable).Where("feed_url = ?", feedURL).Updates(map[string]any{
		"fetched_at": time.Now().Add(-2 * time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}

	items, _, err := loadRemoteItems(db, feedURL, 5, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Title != "Cached" {
		t.Fatalf("stale items = %#v", items)
	}
}

func TestLoadRemoteItemsLimitsCount(t *testing.T) {
	db := openTestDB(t)
	original := fetchFeedBody
	fetchFeedBody = func(url string) ([]byte, error) {
		return []byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>T</title>
<item><title>1</title><link>https://example.test/1</link></item>
<item><title>2</title><link>https://example.test/2</link></item>
<item><title>3</title><link>https://example.test/3</link></item>
</channel></rss>`), nil
	}
	t.Cleanup(func() { fetchFeedBody = original })

	items, _, err := loadRemoteItems(db, "https://example.test/n.xml", 2, 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("len=%d", len(items))
	}
}

func TestConfigRemoteCacheMinutes(t *testing.T) {
	cfg := configFromMap(map[string]any{"remote_cache_minutes": float64(15)})
	if cfg.RemoteCacheMinutes != 15 {
		t.Fatalf("RemoteCacheMinutes = %d", cfg.RemoteCacheMinutes)
	}
	cfg = configFromMap(map[string]any{"remote_cache_minutes": float64(0)})
	if cfg.RemoteCacheMinutes != defaultCacheMinutes {
		t.Fatalf("default RemoteCacheMinutes = %d", cfg.RemoteCacheMinutes)
	}
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rss.sqlite")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateFeedCache(db); err != nil {
		t.Fatal(err)
	}
	return db
}
