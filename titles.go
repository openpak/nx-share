package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// titleCatalog names a game from its application id, from the website's public title list
// (GET /api/v1/titles: {"titles":[{"console","name","title_id","title_ids":[...]}]}). It is
// fetched at most once per refresh interval; a failed fetch keeps the last list. An unknown
// id has no name, and the post goes out without one.
type titleCatalog struct {
	url     string
	client  *http.Client
	refresh time.Duration

	mu      sync.Mutex
	names   map[string]string
	fetched time.Time
}

func newTitleCatalog(url string) *titleCatalog {
	return &titleCatalog{url: url, client: &http.Client{Timeout: 5 * time.Second}, refresh: time.Hour}
}

func (c *titleCatalog) name(ctx context.Context, appID string) string {
	if appID == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.url != "" && (c.names == nil || time.Since(c.fetched) > c.refresh) {
		if names, err := c.fetch(ctx); err != nil {
			log.Printf("%s title catalog: %v", logPrefix, err)
			if c.names == nil {
				c.names = map[string]string{}
			}
		} else {
			c.names = names
		}
		c.fetched = time.Now()
	}
	return c.names[appID]
}

func (c *titleCatalog) fetch(ctx context.Context) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, &verseError{status: res.StatusCode, code: "catalog"}
	}
	var doc struct {
		Titles []struct {
			Console  string   `json:"console"`
			Name     string   `json:"name"`
			TitleID  string   `json:"title_id"`
			TitleIDs []string `json:"title_ids"`
		} `json:"titles"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(&doc); err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, t := range doc.Titles {
		if t.Console != "Switch" || t.Name == "" {
			continue
		}
		for _, id := range append([]string{t.TitleID}, t.TitleIDs...) {
			if id = strings.ToUpper(strings.TrimSpace(id)); id != "" {
				names[id] = t.Name
			}
		}
	}
	return names, nil
}
