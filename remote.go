package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	cacheTable          = "ext_rss_feed_cache"
	defaultCacheMinutes = 60
	maxCacheMinutes     = 10080 // 7 days
	maxFetchBytes       = 2 << 20
	fetchTimeout        = 15 * time.Second
)

type feedCacheRow struct {
	FeedURL   string    `gorm:"primaryKey;column:feed_url;size:2048"`
	Body      string    `gorm:"column:body;type:text"`
	FetchedAt time.Time `gorm:"column:fetched_at;index"`
	LastError string    `gorm:"column:last_error;type:text"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

type remoteItem struct {
	Title       string
	Link        string
	Description string
	Published   time.Time
	PubDate     string
}

type remoteRSS struct {
	XMLName xml.Name       `xml:"rss"`
	Channel remoteChannel  `xml:"channel"`
}

type remoteChannel struct {
	Title       string           `xml:"title"`
	Link        string           `xml:"link"`
	Description string           `xml:"description"`
	Items       []remoteRSSItem  `xml:"item"`
}

type remoteRSSItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	Content     string `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
	PubDate     string `xml:"pubDate"`
	GUID        string `xml:"guid"`
}

type remoteAtom struct {
	XMLName xml.Name         `xml:"feed"`
	Title   string           `xml:"title"`
	Entries []remoteAtomEntry `xml:"entry"`
}

type remoteAtomEntry struct {
	Title     string         `xml:"title"`
	Summary   string         `xml:"summary"`
	Content   remoteAtomText `xml:"content"`
	Published string         `xml:"published"`
	Updated   string         `xml:"updated"`
	ID        string         `xml:"id"`
	Links     []remoteAtomLink `xml:"link"`
}

type remoteAtomText struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type remoteAtomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

// fetchFeedBody downloads a remote feed body. Overridable in tests.
var fetchFeedBody = defaultFetchFeedBody

func migrateFeedCache(db *gorm.DB) error {
	return db.Table(cacheTable).AutoMigrate(&feedCacheRow{})
}

func loadRemoteItems(db *gorm.DB, feedURL string, maxItems, cacheMinutes int) ([]remoteItem, string, error) {
	feedURL = strings.TrimSpace(feedURL)
	if feedURL == "" {
		return nil, "", errors.New("feed url is required")
	}
	if maxItems <= 0 {
		maxItems = 5
	}
	if maxItems > 100 {
		maxItems = 100
	}
	cacheMinutes = normalizeCacheMinutes(cacheMinutes)

	body, err := cachedFeedBody(db, feedURL, cacheMinutes)
	if err != nil {
		return nil, "", err
	}
	items, feedTitle, err := parseRemoteFeed(body)
	if err != nil {
		return nil, "", err
	}
	if len(items) > maxItems {
		items = items[:maxItems]
	}
	return items, feedTitle, nil
}

func normalizeCacheMinutes(minutes int) int {
	if minutes <= 0 {
		return defaultCacheMinutes
	}
	if minutes > maxCacheMinutes {
		return maxCacheMinutes
	}
	return minutes
}

func cachedFeedBody(db *gorm.DB, feedURL string, cacheMinutes int) (string, error) {
	var row feedCacheRow
	err := db.Table(cacheTable).Where("feed_url = ?", feedURL).First(&row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	fresh := err == nil && !row.FetchedAt.IsZero() &&
		time.Since(row.FetchedAt) < time.Duration(cacheMinutes)*time.Minute &&
		strings.TrimSpace(row.Body) != ""
	if fresh {
		return row.Body, nil
	}

	body, fetchErr := fetchFeedBody(feedURL)
	if fetchErr != nil {
		if err == nil && strings.TrimSpace(row.Body) != "" {
			_ = db.Table(cacheTable).Where("feed_url = ?", feedURL).Updates(map[string]any{
				"last_error": fetchErr.Error(),
				"updated_at": time.Now(),
			}).Error
			return row.Body, nil
		}
		return "", fetchErr
	}

	now := time.Now()
	save := feedCacheRow{
		FeedURL:   feedURL,
		Body:      string(body),
		FetchedAt: now,
		LastError: "",
		UpdatedAt: now,
	}
	if err := db.Table(cacheTable).Save(&save).Error; err != nil {
		return "", err
	}
	return save.Body, nil
}

func defaultFetchFeedBody(feedURL string) ([]byte, error) {
	if !strings.HasPrefix(feedURL, "http://") && !strings.HasPrefix(feedURL, "https://") {
		return nil, fmt.Errorf("feed url must be http or https")
	}
	client := &http.Client{Timeout: fetchTimeout}
	req, err := http.NewRequest(http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "cannon-ext-rss/"+extensionVersion)
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml, */*")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("feed request failed: %s", resp.Status)
	}
	limited := io.LimitReader(resp.Body, maxFetchBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > maxFetchBytes {
		return nil, fmt.Errorf("feed exceeds %d bytes", maxFetchBytes)
	}
	return body, nil
}

func parseRemoteFeed(body string) ([]remoteItem, string, error) {
	raw := strings.TrimSpace(body)
	if raw == "" {
		return nil, "", errors.New("empty feed body")
	}

	var rssDoc remoteRSS
	if err := xml.Unmarshal([]byte(raw), &rssDoc); err == nil && (len(rssDoc.Channel.Items) > 0 || strings.TrimSpace(rssDoc.Channel.Title) != "") {
		items := make([]remoteItem, 0, len(rssDoc.Channel.Items))
		for _, item := range rssDoc.Channel.Items {
			published := parseFeedTime(item.PubDate)
			items = append(items, remoteItem{
				Title:       strings.TrimSpace(item.Title),
				Link:        firstNonEmpty(strings.TrimSpace(item.Link), strings.TrimSpace(item.GUID)),
				Description: truncateText(plainText(firstNonEmpty(item.Description, item.Content)), 280),
				Published:   published,
				PubDate:     formatFeedTime(published),
			})
		}
		return items, strings.TrimSpace(rssDoc.Channel.Title), nil
	}

	var atom remoteAtom
	if err := xml.Unmarshal([]byte(raw), &atom); err != nil {
		return nil, "", fmt.Errorf("unsupported feed format: %w", err)
	}
	items := make([]remoteItem, 0, len(atom.Entries))
	for _, entry := range atom.Entries {
		published := parseFeedTime(firstNonEmpty(entry.Published, entry.Updated))
		items = append(items, remoteItem{
			Title:       strings.TrimSpace(entry.Title),
			Link:        atomEntryLink(entry),
			Description: truncateText(plainText(firstNonEmpty(entry.Summary, entry.Content.Value)), 280),
			Published:   published,
			PubDate:     formatFeedTime(published),
		})
	}
	return items, strings.TrimSpace(atom.Title), nil
}

func atomEntryLink(entry remoteAtomEntry) string {
	var alternate, anyLink string
	for _, link := range entry.Links {
		href := strings.TrimSpace(link.Href)
		if href == "" {
			continue
		}
		if anyLink == "" {
			anyLink = href
		}
		rel := strings.ToLower(strings.TrimSpace(link.Rel))
		if rel == "" || rel == "alternate" {
			alternate = href
			break
		}
	}
	if alternate != "" {
		return alternate
	}
	if anyLink != "" {
		return anyLink
	}
	return strings.TrimSpace(entry.ID)
}

func parseFeedTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC3339,
		time.RFC3339Nano,
		"Mon, 02 Jan 2006 15:04:05 MST",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

func formatFeedTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("Jan 2, 2006")
}
