package main

import (
	"embed"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rob121/cannon/extension"
	"gorm.io/gorm"
)

//go:embed all:templates
var templateFiles embed.FS

const (
	extensionName        = "cannon-ext-rss"
	extensionTitle       = "RSS Feed"
	extensionDescription = "Publishes selected Cannon content as RSS/Atom, and embeds remote feeds in template blocks with a configurable cache lifetime."
	extensionVersion     = "0.3.0"
	updateURLBase        = "https://github.com/rob121/cannon-ext-rss/releases/download"
	defaultAdminMenu     = "RSS Feed"

	configTable      = "ext_rss_configuration"
	configSection    = "feed"
	defaultRoutePath = "/rss"
	feedEndpointID   = "feed"
)

type rssApp struct {
	db        *gorm.DB
	store     extension.ConfigurationStore
	templates *extension.Templates
}

type categoryRow struct {
	CategoryID uint   `gorm:"column:category_id"`
	ParentID   *uint  `gorm:"column:parent_id"`
	Name       string `gorm:"column:name"`
	Status     string `gorm:"column:status"`
}

type itemRow struct {
	ItemID          uint       `gorm:"column:item_id"`
	Locale          string     `gorm:"column:locale"`
	Title           string     `gorm:"column:title"`
	Slug            string     `gorm:"column:slug"`
	Intro           string     `gorm:"column:intro"`
	Body            string     `gorm:"column:body"`
	MetaDescription string     `gorm:"column:meta_description"`
	CanonicalURL    string     `gorm:"column:canonical_url"`
	Image           string     `gorm:"column:image"`
	PublishStart    *time.Time `gorm:"column:publish_start"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
	CategoryID      *uint      `gorm:"column:category_id"`
	CategoryName    string     `gorm:"column:category_name"`
	AuthorUsername  string     `gorm:"column:author_username"`
	AuthorEmail     string     `gorm:"column:author_email"`
	AuthorGivenName string     `gorm:"column:author_given_name"`
	AuthorFamily    string     `gorm:"column:author_family_name"`
}

type feedConfig struct {
	Enabled                 bool
	Format                  string
	Title                   string
	Description             string
	SiteURL                 string
	FeedPath                string
	Language                string
	Copyright               string
	ManagingEditor          string
	WebMaster               string
	TTLMinutes              int
	MaxItems                int
	RemoteCacheMinutes      int
	CategoryIDs             []uint
	IncludeDescendants      bool
	RequirePublicVisibility bool
	DescriptionSource       string
	DescriptionLimit        int
	IncludeContentEncoded   bool
	IncludeImageEnclosures  bool
	Sort                    string
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func extensionInfo() extension.Info {
	return extension.Info{
		Name:          extensionName,
		Title:         extensionTitle,
		Description:   extensionDescription,
		Version:       extensionVersion,
		UpdateURLBase: updateURLBase,
		AdminMenuName: defaultAdminMenu,
	}
}

func run() error {
	if handled, err := extension.WriteDataArg(extensionInfo(), os.Args, os.Stdout); handled {
		return err
	}
	db, site, err := extension.OpenDB()
	if err != nil {
		return err
	}
	if err := extension.MigrateConfigurationStore(db, configTable); err != nil {
		return fmt.Errorf("migrate configuration: %w", err)
	}
	if err := migrateFeedCache(db); err != nil {
		return fmt.Errorf("migrate feed cache: %w", err)
	}

	store := extension.DBConfigurationStore(db, configTable)
	app := &rssApp{
		db:        db,
		store:     store,
		templates: extension.NewTemplates(templateFiles, "templates"),
	}

	s := extension.New(extensionInfo())
	s.EmbedTemplates(templateFiles, "templates")
	s.OnConfiguration(extension.MapConfiguration(configurationDefinitions(), store))
	s.OnInstall(func(req extension.WireRequest) error {
		if err := extension.MigrateConfigurationStore(db, configTable); err != nil {
			return err
		}
		if err := migrateFeedCache(db); err != nil {
			return err
		}
		return ensureRSSRoute(db)
	})
	s.OnBlockList(app.blockDefinitions)
	s.RegisterBlock(app.blockDefinition(), app.block)
	s.RegisterEndpoint(extension.EndpointDefinition{
		ID:    feedEndpointID,
		Title: "RSS / Atom Feed",
	}, func(item string, req extension.WireRequest) extension.WireResponse {
		cfg, err := loadConfig(store)
		if err != nil {
			return extension.Error(http.StatusInternalServerError, err.Error())
		}
		cfg = mergeEndpointData(cfg, req.EndpointData)
		if !cfg.Enabled {
			return extension.Error(http.StatusNotFound, "feed disabled")
		}
		baseURL := resolveBaseURL(cfg, req, site.Host)
		items, err := loadFeedItems(db, cfg)
		if err != nil {
			return extension.Error(http.StatusInternalServerError, err.Error())
		}
		siteName := strings.TrimSpace(site.Name)
		if siteName == "" {
			siteName = "Cannon"
		}
		raw, contentType, err := buildFeed(siteName, baseURL, cfg, items)
		if err != nil {
			return extension.Error(http.StatusInternalServerError, err.Error())
		}
		return extension.WireResponse{
			StatusCode: http.StatusOK,
			Header: map[string][]string{
				"Content-Type":  {contentType},
				"Cache-Control": {"public, max-age=300"},
			},
			Body: string(raw),
		}
	})
	return s.Run()
}

func configurationDefinitions() []extension.ConfigurationDefinition {
	schema := []byte(`{
  "type": "object",
  "properties": {
    "enabled": {"type": "boolean", "title": "Enabled", "default": true},
    "format": {"type": "string", "title": "Feed format", "enum": ["rss", "atom"], "default": "rss"},
    "title": {"type": "string", "title": "Feed title"},
    "description": {"type": "string", "title": "Feed description"},
    "site_url": {"type": "string", "title": "Canonical site URL"},
    "feed_path": {"type": "string", "title": "Feed path", "default": "/rss"},
    "language": {"type": "string", "title": "Language", "default": "en-us"},
    "copyright": {"type": "string", "title": "Copyright"},
    "managing_editor": {"type": "string", "title": "Managing editor email"},
    "webmaster": {"type": "string", "title": "Webmaster email"},
    "ttl_minutes": {"type": "integer", "title": "TTL minutes", "default": 60},
    "max_items": {"type": "integer", "title": "Maximum items", "default": 25},
    "remote_cache_minutes": {"type": "integer", "title": "Remote feed cache minutes", "default": 60, "description": "Default lifetime for cached remote feed URLs used by the RSS block."},
    "include_descendants": {"type": "boolean", "title": "Include descendant categories", "default": true},
    "require_public_visibility": {"type": "boolean", "title": "Require public visibility", "default": true},
    "description_source": {"type": "string", "title": "Description source", "enum": ["intro", "meta", "body", "intro_body"], "default": "intro_body"},
    "description_limit": {"type": "integer", "title": "Description character limit", "default": 512},
    "include_content_encoded": {"type": "boolean", "title": "Include full content", "default": false},
    "include_image_enclosures": {"type": "boolean", "title": "Include image enclosures", "default": true},
    "sort": {"type": "string", "title": "Sort", "enum": ["newest", "updated", "sort"], "default": "newest"},
    "category_ids": {"type": "string", "title": "Additional category IDs"},
    "category_1_id": {"type": ["integer", "null"], "format": "category", "title": "Category 1"},
    "category_2_id": {"type": ["integer", "null"], "format": "category", "title": "Category 2"},
    "category_3_id": {"type": ["integer", "null"], "format": "category", "title": "Category 3"},
    "category_4_id": {"type": ["integer", "null"], "format": "category", "title": "Category 4"},
    "category_5_id": {"type": ["integer", "null"], "format": "category", "title": "Category 5"},
    "category_6_id": {"type": ["integer", "null"], "format": "category", "title": "Category 6"},
    "category_7_id": {"type": ["integer", "null"], "format": "category", "title": "Category 7"},
    "category_8_id": {"type": ["integer", "null"], "format": "category", "title": "Category 8"},
    "category_9_id": {"type": ["integer", "null"], "format": "category", "title": "Category 9"},
    "category_10_id": {"type": ["integer", "null"], "format": "category", "title": "Category 10"}
  }
}`)
	uischema := []byte(`{
  "type": "VerticalLayout",
  "elements": [
    {"type": "Control", "scope": "#/properties/enabled"},
    {"type": "Control", "scope": "#/properties/format"},
    {"type": "Control", "scope": "#/properties/title"},
    {"type": "Control", "scope": "#/properties/description", "options": {"multi": true}},
    {"type": "Control", "scope": "#/properties/site_url"},
    {"type": "Control", "scope": "#/properties/feed_path"},
    {"type": "Control", "scope": "#/properties/language"},
    {"type": "Control", "scope": "#/properties/copyright"},
    {"type": "Control", "scope": "#/properties/managing_editor"},
    {"type": "Control", "scope": "#/properties/webmaster"},
    {"type": "Control", "scope": "#/properties/ttl_minutes"},
    {"type": "Control", "scope": "#/properties/max_items"},
    {"type": "Control", "scope": "#/properties/remote_cache_minutes"},
    {"type": "Control", "scope": "#/properties/sort"},
    {"type": "Control", "scope": "#/properties/description_source"},
    {"type": "Control", "scope": "#/properties/description_limit"},
    {"type": "Control", "scope": "#/properties/include_descendants"},
    {"type": "Control", "scope": "#/properties/require_public_visibility"},
    {"type": "Control", "scope": "#/properties/include_content_encoded"},
    {"type": "Control", "scope": "#/properties/include_image_enclosures"},
    {"type": "Control", "scope": "#/properties/category_ids"},
    {"type": "Control", "scope": "#/properties/category_1_id", "options": {"format": "category"}},
    {"type": "Control", "scope": "#/properties/category_2_id", "options": {"format": "category"}},
    {"type": "Control", "scope": "#/properties/category_3_id", "options": {"format": "category"}},
    {"type": "Control", "scope": "#/properties/category_4_id", "options": {"format": "category"}},
    {"type": "Control", "scope": "#/properties/category_5_id", "options": {"format": "category"}},
    {"type": "Control", "scope": "#/properties/category_6_id", "options": {"format": "category"}},
    {"type": "Control", "scope": "#/properties/category_7_id", "options": {"format": "category"}},
    {"type": "Control", "scope": "#/properties/category_8_id", "options": {"format": "category"}},
    {"type": "Control", "scope": "#/properties/category_9_id", "options": {"format": "category"}},
    {"type": "Control", "scope": "#/properties/category_10_id", "options": {"format": "category"}}
  ]
}`)
	return []extension.ConfigurationDefinition{{
		ID:       configSection,
		Title:    "Feed",
		Schema:   schema,
		UISchema: uischema,
	}}
}

func loadConfig(store extension.ConfigurationStore) (feedConfig, error) {
	data, err := store.Load(configSection)
	if err != nil {
		return feedConfig{}, err
	}
	return configFromMap(data), nil
}

func configFromMap(data map[string]any) feedConfig {
	cfg := feedConfig{
		Enabled:                 boolDefault(data, "enabled", true),
		Format:                  stringDefault(data, "format", "rss"),
		Title:                   strings.TrimSpace(stringValue(data, "title")),
		Description:             strings.TrimSpace(stringValue(data, "description")),
		SiteURL:                 strings.TrimSpace(stringValue(data, "site_url")),
		FeedPath:                stringDefault(data, "feed_path", defaultRoutePath),
		Language:                stringDefault(data, "language", "en-us"),
		Copyright:               strings.TrimSpace(stringValue(data, "copyright")),
		ManagingEditor:          strings.TrimSpace(stringValue(data, "managing_editor")),
		WebMaster:               strings.TrimSpace(stringValue(data, "webmaster")),
		TTLMinutes:              intDefault(data, "ttl_minutes", 60),
		MaxItems:                intDefault(data, "max_items", 25),
		RemoteCacheMinutes:      intDefault(data, "remote_cache_minutes", defaultCacheMinutes),
		IncludeDescendants:      boolDefault(data, "include_descendants", true),
		RequirePublicVisibility: boolDefault(data, "require_public_visibility", true),
		DescriptionSource:       stringDefault(data, "description_source", "intro_body"),
		DescriptionLimit:        intDefault(data, "description_limit", 512),
		IncludeContentEncoded:   boolDefault(data, "include_content_encoded", false),
		IncludeImageEnclosures:  boolDefault(data, "include_image_enclosures", true),
		Sort:                    stringDefault(data, "sort", "newest"),
	}
	for _, id := range parseCategoryIDs(stringValue(data, "category_ids")) {
		cfg.CategoryIDs = append(cfg.CategoryIDs, id)
	}
	for i := 1; i <= 10; i++ {
		if id := uintValue(data, fmt.Sprintf("category_%d_id", i)); id > 0 {
			cfg.CategoryIDs = append(cfg.CategoryIDs, id)
		}
	}
	cfg.CategoryIDs = uniqueUint(cfg.CategoryIDs)
	if cfg.MaxItems <= 0 {
		cfg.MaxItems = 25
	}
	if cfg.MaxItems > 500 {
		cfg.MaxItems = 500
	}
	cfg.RemoteCacheMinutes = normalizeCacheMinutes(cfg.RemoteCacheMinutes)
	if cfg.DescriptionLimit <= 0 {
		cfg.DescriptionLimit = 512
	}
	if cfg.FeedPath == "" {
		cfg.FeedPath = defaultRoutePath
	}
	if !strings.HasPrefix(cfg.FeedPath, "/") {
		cfg.FeedPath = "/" + cfg.FeedPath
	}
	if cfg.Format != "atom" {
		cfg.Format = "rss"
	}
	switch cfg.DescriptionSource {
	case "intro", "meta", "body", "intro_body":
	default:
		cfg.DescriptionSource = "intro_body"
	}
	switch cfg.Sort {
	case "updated", "sort":
	default:
		cfg.Sort = "newest"
	}
	return cfg
}

func mergeEndpointData(cfg feedConfig, data map[string]any) feedConfig {
	if len(data) == 0 {
		return cfg
	}
	overlay := configFromMap(data)
	if v := strings.TrimSpace(stringValue(data, "format")); v != "" {
		cfg.Format = overlay.Format
	}
	if v := strings.TrimSpace(stringValue(data, "title")); v != "" {
		cfg.Title = v
	}
	if v := strings.TrimSpace(stringValue(data, "description")); v != "" {
		cfg.Description = v
	}
	if v := strings.TrimSpace(stringValue(data, "site_url")); v != "" {
		cfg.SiteURL = v
	}
	if v := strings.TrimSpace(stringValue(data, "feed_path")); v != "" {
		cfg.FeedPath = overlay.FeedPath
	}
	if len(overlay.CategoryIDs) > 0 {
		cfg.CategoryIDs = overlay.CategoryIDs
	}
	if _, ok := data["max_items"]; ok {
		cfg.MaxItems = overlay.MaxItems
	}
	return cfg
}

func loadFeedItems(db *gorm.DB, cfg feedConfig) ([]itemRow, error) {
	categoryIDs := cfg.CategoryIDs
	if len(categoryIDs) > 0 && cfg.IncludeDescendants {
		ids, err := expandCategoryIDs(db, categoryIDs, cfg.RequirePublicVisibility)
		if err != nil {
			return nil, err
		}
		categoryIDs = ids
	}

	now := time.Now()
	q := db.Table("items").
		Select(`items.item_id, items.locale, items.title, items.slug, items.intro, items.body,
			items.meta_description, items.canonical_url, items.image, items.publish_start,
			items.created_at, items.updated_at, items.category_id,
			categories.name AS category_name,
			users.username AS author_username, users.email AS author_email,
			users.given_name AS author_given_name, users.family_name AS author_family_name`).
		Joins("LEFT JOIN categories ON categories.category_id = items.category_id").
		Joins("LEFT JOIN users ON users.user_id = items.author_id").
		Where("items.status = ?", "published").
		Where("(items.publish_start IS NULL OR items.publish_start <= ?)", now).
		Where("(items.publish_end IS NULL OR items.publish_end >= ?)", now)

	if len(categoryIDs) > 0 {
		q = q.Where("items.category_id IN ?", categoryIDs)
	}
	if cfg.RequirePublicVisibility {
		q = applyPublicVisibility(q)
	}
	switch cfg.Sort {
	case "updated":
		q = q.Order("items.updated_at DESC, items.created_at DESC")
	case "sort":
		q = q.Order("items.sort ASC, COALESCE(items.publish_start, items.created_at) DESC, items.created_at DESC")
	default:
		q = q.Order("COALESCE(items.publish_start, items.created_at) DESC, items.created_at DESC")
	}

	var rows []itemRow
	if err := q.Limit(cfg.MaxItems).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func applyPublicVisibility(q *gorm.DB) *gorm.DB {
	publicGroupSubquery := "SELECT group_id FROM groups WHERE name = ? AND status = ?"
	return q.
		Where(`(
			NOT EXISTS (SELECT 1 FROM item_groups ig WHERE ig.item_item_id = items.item_id)
			OR EXISTS (
				SELECT 1 FROM item_groups ig
				WHERE ig.item_item_id = items.item_id
				AND ig.group_group_id IN (`+publicGroupSubquery+`)
			)
		)`, "public", "active").
		Where(`(
			items.category_id IS NULL
			OR NOT EXISTS (SELECT 1 FROM category_groups cg WHERE cg.category_category_id = items.category_id)
			OR EXISTS (
				SELECT 1 FROM category_groups cg
				WHERE cg.category_category_id = items.category_id
				AND cg.group_group_id IN (`+publicGroupSubquery+`)
			)
		)`, "public", "active")
}

func expandCategoryIDs(db *gorm.DB, roots []uint, requirePublic bool) ([]uint, error) {
	var rows []categoryRow
	q := db.Table("categories").
		Select("category_id, parent_id, name, status").
		Where("status = ?", "active")
	if requirePublic {
		q = q.Where(`(
			NOT EXISTS (SELECT 1 FROM category_groups cg WHERE cg.category_category_id = categories.category_id)
			OR EXISTS (
				SELECT 1 FROM category_groups cg
				JOIN groups ON groups.group_id = cg.group_group_id
				WHERE cg.category_category_id = categories.category_id
				AND groups.name = ? AND groups.status = ?
			)
		)`, "public", "active")
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	children := map[uint][]uint{}
	active := map[uint]bool{}
	for _, row := range rows {
		active[row.CategoryID] = true
		if row.ParentID != nil && *row.ParentID > 0 {
			children[*row.ParentID] = append(children[*row.ParentID], row.CategoryID)
		}
	}
	out := make([]uint, 0, len(roots))
	seen := map[uint]bool{}
	var walk func(uint)
	walk = func(id uint) {
		if seen[id] || !active[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
		for _, childID := range children[id] {
			walk(childID)
		}
	}
	for _, id := range roots {
		walk(id)
	}
	return out, nil
}

func ensureRSSRoute(db *gorm.DB) error {
	var existing struct {
		RouteID             uint   `gorm:"column:route_id"`
		ExtensionName       string `gorm:"column:extension_name"`
		ExtensionEndpointID string `gorm:"column:extension_endpoint_id"`
	}
	err := db.Table("routes").Select("route_id, extension_name, extension_endpoint_id").
		Where("path = ?", defaultRoutePath).First(&existing).Error
	if err == nil {
		if existing.ExtensionName == extensionName && existing.ExtensionEndpointID != feedEndpointID {
			return db.Table("routes").Where("route_id = ?", existing.RouteID).
				Updates(map[string]any{"extension_endpoint_id": feedEndpointID, "type": "Extension Endpoint", "status": "active"}).Error
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	row := map[string]any{
		"name":                  "RSS Feed",
		"path":                  defaultRoutePath,
		"type":                  "Extension Endpoint",
		"status":                "active",
		"extension_name":        extensionName,
		"extension_endpoint_id": feedEndpointID,
		"show_title":            false,
		"sort":                  0,
	}
	if err := db.Table("routes").Create(row).Error; err != nil {
		return err
	}
	var routeID uint
	if err := db.Table("routes").Select("route_id").Where("path = ?", defaultRoutePath).Scan(&routeID).Error; err != nil {
		return err
	}
	if routeID == 0 {
		return nil
	}
	var publicID uint
	if err := db.Table("groups").Select("group_id").
		Where("name = ? AND status = ?", "public", "active").
		Scan(&publicID).Error; err != nil {
		return err
	}
	if publicID == 0 {
		return nil
	}
	return db.Table("route_groups").Create(map[string]any{
		"route_route_id": routeID,
		"group_group_id": publicID,
	}).Error
}

func resolveBaseURL(cfg feedConfig, req extension.WireRequest, siteHost string) string {
	if base := normalizeBaseURL(cfg.SiteURL); base != "" {
		return base
	}
	host := headerFirst(req.Header, "Host")
	proto := headerFirst(req.Header, "X-Forwarded-Proto")
	if proto == "" {
		proto = headerFirst(req.Header, "X-Forwarded-Protocol")
	}
	if proto == "" {
		proto = "https"
	}
	if host != "" {
		return normalizeBaseURL(proto + "://" + host)
	}
	if base := normalizeBaseURL(siteHost); base != "" {
		return base
	}
	return "http://localhost"
}

func normalizeBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	return "https://" + raw
}

func headerFirst(header map[string][]string, key string) string {
	for k, values := range header {
		if strings.EqualFold(k, key) && len(values) > 0 {
			return strings.TrimSpace(strings.Split(values[0], ",")[0])
		}
	}
	return ""
}

func buildFeed(siteName, baseURL string, cfg feedConfig, items []itemRow) ([]byte, string, error) {
	title := cfg.Title
	if title == "" {
		title = siteName
	}
	desc := cfg.Description
	if desc == "" {
		desc = "Latest content from " + siteName
	}
	feedURL := absoluteURL(baseURL, cfg.FeedPath)
	if cfg.Format == "atom" {
		raw, err := buildAtom(title, desc, feedURL, baseURL, cfg, items)
		return raw, "application/atom+xml; charset=utf-8", err
	}
	raw, err := buildRSS(title, desc, feedURL, baseURL, cfg, items)
	return raw, "application/rss+xml; charset=utf-8", err
}

type rssDoc struct {
	XMLName   xml.Name   `xml:"rss"`
	Version   string     `xml:"version,attr"`
	AtomNS    string     `xml:"xmlns:atom,attr,omitempty"`
	ContentNS string     `xml:"xmlns:content,attr,omitempty"`
	Channel   rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title          string       `xml:"title"`
	Link           string       `xml:"link"`
	Description    string       `xml:"description"`
	Language       string       `xml:"language,omitempty"`
	Copyright      string       `xml:"copyright,omitempty"`
	ManagingEditor string       `xml:"managingEditor,omitempty"`
	WebMaster      string       `xml:"webMaster,omitempty"`
	LastBuildDate  string       `xml:"lastBuildDate,omitempty"`
	TTL            int          `xml:"ttl,omitempty"`
	AtomLink       *rssAtomLink `xml:"atom:link,omitempty"`
	Items          []rssItem    `xml:"item"`
}

type rssAtomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type rssItem struct {
	Title       string             `xml:"title"`
	Link        string             `xml:"link"`
	Description string             `xml:"description"`
	GUID        rssGUID            `xml:"guid"`
	PubDate     string             `xml:"pubDate,omitempty"`
	Author      string             `xml:"author,omitempty"`
	Category    string             `xml:"category,omitempty"`
	Enclosure   *rssEnclosure      `xml:"enclosure,omitempty"`
	Content     *rssContentEncoded `xml:"content:encoded,omitempty"`
}

type rssGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Type   string `xml:"type,attr,omitempty"`
	Length string `xml:"length,attr,omitempty"`
}

type rssContentEncoded struct {
	Value string `xml:",chardata"`
}

func buildRSS(title, desc, feedURL, baseURL string, cfg feedConfig, items []itemRow) ([]byte, error) {
	channel := rssChannel{
		Title:          title,
		Link:           baseURL,
		Description:    desc,
		Language:       cfg.Language,
		Copyright:      cfg.Copyright,
		ManagingEditor: cfg.ManagingEditor,
		WebMaster:      cfg.WebMaster,
		LastBuildDate:  lastBuild(items).Format(time.RFC1123Z),
		TTL:            cfg.TTLMinutes,
		AtomLink:       &rssAtomLink{Href: feedURL, Rel: "self", Type: "application/rss+xml"},
		Items:          make([]rssItem, 0, len(items)),
	}
	for _, item := range items {
		link := itemLink(baseURL, item)
		entry := rssItem{
			Title:       item.Title,
			Link:        link,
			Description: itemDescription(item, cfg),
			GUID:        rssGUID{IsPermaLink: "true", Value: link},
			PubDate:     itemPublishTime(item).Format(time.RFC1123Z),
			Author:      authorString(item),
			Category:    item.CategoryName,
		}
		if cfg.IncludeContentEncoded {
			entry.Content = &rssContentEncoded{Value: itemContent(item)}
		}
		if cfg.IncludeImageEnclosures {
			if image := strings.TrimSpace(item.Image); image != "" {
				entry.Enclosure = &rssEnclosure{URL: absoluteURL(baseURL, image), Type: guessImageType(image)}
			}
		}
		channel.Items = append(channel.Items, entry)
	}
	doc := rssDoc{
		Version:   "2.0",
		AtomNS:    "http://www.w3.org/2005/Atom",
		ContentNS: "http://purl.org/rss/1.0/modules/content/",
		Channel:   channel,
	}
	raw, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), raw...), nil
}

type atomFeed struct {
	XMLName  xml.Name    `xml:"feed"`
	XMLNS    string      `xml:"xmlns,attr"`
	Title    string      `xml:"title"`
	Subtitle string      `xml:"subtitle,omitempty"`
	ID       string      `xml:"id"`
	Updated  string      `xml:"updated"`
	Links    []atomLink  `xml:"link"`
	Rights   string      `xml:"rights,omitempty"`
	Entries  []atomEntry `xml:"entry"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
	Type string `xml:"type,attr,omitempty"`
}

type atomEntry struct {
	Title     string        `xml:"title"`
	ID        string        `xml:"id"`
	Updated   string        `xml:"updated"`
	Published string        `xml:"published,omitempty"`
	Link      atomLink      `xml:"link"`
	Summary   string        `xml:"summary,omitempty"`
	Content   *atomContent  `xml:"content,omitempty"`
	Author    *atomAuthor   `xml:"author,omitempty"`
	Category  *atomCategory `xml:"category,omitempty"`
}

type atomContent struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type atomAuthor struct {
	Name  string `xml:"name"`
	Email string `xml:"email,omitempty"`
}

type atomCategory struct {
	Term string `xml:"term,attr"`
}

func buildAtom(title, desc, feedURL, baseURL string, cfg feedConfig, items []itemRow) ([]byte, error) {
	feed := atomFeed{
		XMLNS:    "http://www.w3.org/2005/Atom",
		Title:    title,
		Subtitle: desc,
		ID:       feedURL,
		Updated:  lastBuild(items).Format(time.RFC3339),
		Links: []atomLink{
			{Href: baseURL, Rel: "alternate", Type: "text/html"},
			{Href: feedURL, Rel: "self", Type: "application/atom+xml"},
		},
		Rights:  cfg.Copyright,
		Entries: make([]atomEntry, 0, len(items)),
	}
	for _, item := range items {
		link := itemLink(baseURL, item)
		entry := atomEntry{
			Title:     item.Title,
			ID:        link,
			Updated:   item.UpdatedAt.Format(time.RFC3339),
			Published: itemPublishTime(item).Format(time.RFC3339),
			Link:      atomLink{Href: link, Rel: "alternate", Type: "text/html"},
			Summary:   itemDescription(item, cfg),
		}
		if cfg.IncludeContentEncoded {
			entry.Content = &atomContent{Type: "html", Value: itemContent(item)}
		}
		if name := authorName(item); name != "" {
			entry.Author = &atomAuthor{Name: name, Email: item.AuthorEmail}
		}
		if item.CategoryName != "" {
			entry.Category = &atomCategory{Term: item.CategoryName}
		}
		feed.Entries = append(feed.Entries, entry)
	}
	raw, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), raw...), nil
}

func itemLink(baseURL string, item itemRow) string {
	if canonical := strings.TrimSpace(item.CanonicalURL); canonical != "" {
		return absoluteURL(baseURL, canonical)
	}
	return absoluteURL(baseURL, "/content/item/"+strings.TrimPrefix(item.Slug, "/"))
}

func absoluteURL(baseURL, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return baseURL
	}
	if u, err := url.Parse(path); err == nil && u.IsAbs() {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.TrimRight(baseURL, "/") + path
}

func itemDescription(item itemRow, cfg feedConfig) string {
	var text string
	switch cfg.DescriptionSource {
	case "meta":
		text = firstNonEmpty(item.MetaDescription, item.Intro, item.Body)
	case "body":
		text = firstNonEmpty(item.Body, item.Intro, item.MetaDescription)
	case "intro":
		text = firstNonEmpty(item.Intro, item.MetaDescription, item.Body)
	default:
		text = firstNonEmpty(item.Intro, item.Body, item.MetaDescription)
	}
	return truncateText(plainText(text), cfg.DescriptionLimit)
}

func itemContent(item itemRow) string {
	return firstNonEmpty(item.Body, item.Intro)
}

var tagRE = regexp.MustCompile(`<[^>]+>`)

func plainText(raw string) string {
	raw = html.UnescapeString(raw)
	raw = tagRE.ReplaceAllString(raw, " ")
	return strings.Join(strings.Fields(raw), " ")
}

func truncateText(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func itemPublishTime(item itemRow) time.Time {
	if item.PublishStart != nil && !item.PublishStart.IsZero() {
		return *item.PublishStart
	}
	return item.CreatedAt
}

func lastBuild(items []itemRow) time.Time {
	var latest time.Time
	for _, item := range items {
		if item.UpdatedAt.After(latest) {
			latest = item.UpdatedAt
		}
	}
	if latest.IsZero() {
		return time.Now()
	}
	return latest
}

func authorString(item itemRow) string {
	name := authorName(item)
	if item.AuthorEmail == "" {
		return name
	}
	if name == "" {
		return item.AuthorEmail
	}
	return item.AuthorEmail + " (" + name + ")"
}

func authorName(item itemRow) string {
	name := strings.TrimSpace(strings.TrimSpace(item.AuthorGivenName) + " " + strings.TrimSpace(item.AuthorFamily))
	if name != "" {
		return name
	}
	return strings.TrimSpace(item.AuthorUsername)
}

func guessImageType(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

func parseCategoryIDs(raw string) []uint {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\t' || r == ' '
	})
	out := make([]uint, 0, len(fields))
	for _, field := range fields {
		id, err := strconv.ParseUint(strings.TrimSpace(field), 10, 64)
		if err == nil && id > 0 {
			out = append(out, uint(id))
		}
	}
	return out
}

func uniqueUint(ids []uint) []uint {
	seen := map[uint]bool{}
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func stringValue(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	v, ok := data[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func stringDefault(data map[string]any, key, fallback string) string {
	if value := strings.TrimSpace(stringValue(data, key)); value != "" {
		return value
	}
	return fallback
}

func boolDefault(data map[string]any, key string, fallback bool) bool {
	if data == nil {
		return fallback
	}
	v, ok := data[key]
	if !ok || v == nil {
		return fallback
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		if parsed, err := strconv.ParseBool(strings.TrimSpace(t)); err == nil {
			return parsed
		}
	case float64:
		return t != 0
	case int:
		return t != 0
	}
	return fallback
}

func intDefault(data map[string]any, key string, fallback int) int {
	if data == nil {
		return fallback
	}
	v, ok := data[key]
	if !ok || v == nil {
		return fallback
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case uint64:
		return int(t)
	case json.Number:
		n, err := t.Int64()
		if err == nil {
			return int(n)
		}
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err == nil {
			return n
		}
	}
	return fallback
}

func uintValue(data map[string]any, key string) uint {
	if data == nil {
		return 0
	}
	v, ok := data[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		if t > 0 {
			return uint(t)
		}
	case int:
		if t > 0 {
			return uint(t)
		}
	case int64:
		if t > 0 {
			return uint(t)
		}
	case uint:
		return t
	case uint64:
		return uint(t)
	case string:
		id, err := strconv.ParseUint(strings.TrimSpace(t), 10, 64)
		if err == nil && id > 0 {
			return uint(id)
		}
	}
	return 0
}
