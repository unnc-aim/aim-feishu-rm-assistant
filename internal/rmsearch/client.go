// Package rmsearch is a client of the rm-search production search API.
//
// rm-search proxies the Meilisearch HTTP API at {base}/api/ms, so a search
// is a POST to /api/ms/indexes/rm-search/search with a JSON body.
package rmsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Source constants of the rm-search index.
const (
	SourceAnnounce  = "官网公告"
	SourceArticle   = "论坛文章"
	SourceFAQ       = "论坛问答"
	SourceWiki      = "论坛专栏"
	SourcePDF       = "PDF附件"
	ForumSourceExpr = `["` + SourceArticle + `","` + SourceFAQ + `","` + SourceWiki + `"]`
)

// Document is a hit returned by the search API. Field names follow the
// rm-search index schema.
type Document struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Source       string   `json:"source"`
	Title        string   `json:"title"`
	Content      string   `json:"content"`
	URL          string   `json:"url"`
	Season       string   `json:"season"`
	CategoryLvl0 []string `json:"category_lvl0"`
	Author       string   `json:"author_nickname"`
	CreateTimeMS int64    `json:"create_time"`
	UpdateTimeMS int64    `json:"update_time"`
}

// SearchRequest is the request body of the Meilisearch search endpoint.
type SearchRequest struct {
	Q                    string   `json:"q"`
	Filter               string   `json:"filter,omitempty"`
	Sort                 []string `json:"sort,omitempty"`
	Limit                int      `json:"limit,omitempty"`
	Offset               int      `json:"offset,omitempty"`
	AttributesToRetrieve []string `json:"attributesToRetrieve,omitempty"`
}

type searchResponse struct {
	Hits             []Document `json:"hits"`
	Total            int64      `json:"estimatedTotalHits"`
	ProcessingTimeMS int        `json:"processingTimeMs"`
	// Meilisearch errors carry message/code instead of hits; a 200 with
	// these fields is an error and must not read as zero results.
	Message string `json:"message"`
	Code    string `json:"code"`
}

// SearchResult carries the hits of one page plus the estimated total.
type SearchResult struct {
	Hits  []Document
	Total int64
}

// Client talks to a rm-search deployment.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a client for the given base URL, e.g. https://search.scutbot.cn.
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Search performs one search request.
func (c *Client) Search(ctx context.Context, req *SearchRequest) (*SearchResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	url := c.BaseURL + "/api/ms/indexes/rm-search/search"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, truncate(string(data), 200))
	}

	var sr searchResponse
	if err := json.Unmarshal(data, &sr); err != nil {
		return nil, fmt.Errorf("decode body: %w", err)
	}
	if sr.Message != "" {
		return nil, fmt.Errorf("search api error %s: %s", sr.Code, sr.Message)
	}
	return &SearchResult{Hits: sr.Hits, Total: sr.Total}, nil
}

// LatestSince returns documents of the given sources created after tsMS
// (unix milliseconds), newest first.
func (c *Client) LatestSince(ctx context.Context, sources []string, tsMS int64, limit int) ([]Document, error) {
	filter := fmt.Sprintf("(create_time > %d AND source IN [%s])", tsMS, quoteJoin(sources))
	res, err := c.Search(ctx, &SearchRequest{
		Q:      "",
		Filter: filter,
		Sort:   []string{"create_time:desc"},
		Limit:  limit,
		AttributesToRetrieve: []string{
			"id", "type", "source", "title", "content", "url", "season",
			"category_lvl0", "author_nickname", "create_time", "update_time",
		},
	})
	if err != nil {
		return nil, err
	}
	return res.Hits, nil
}

func quoteJoin(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, it := range items {
		quoted = append(quoted, `"`+it+`"`)
	}
	return strings.Join(quoted, ",")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
