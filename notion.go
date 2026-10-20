package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	apiBase    = "https://api.notion.com/v1"
	apiVersion = "2026-03-11"
)

// Client is a minimal read-only Notion REST client: ~3 req/s, retries on 429/5xx.
type Client struct {
	token string
	http  *http.Client
	mu    sync.Mutex
	last  time.Time
	min   time.Duration
	Calls int
}

func NewClient(token string) *Client {
	return &Client{token: token, http: &http.Client{Timeout: 90 * time.Second}, min: 350 * time.Millisecond}
}

type apiError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string {
	return fmt.Sprintf("notion %d %s: %s", e.Status, e.Code, e.Message)
}

func (c *Client) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = b
	}
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		c.mu.Lock()
		if wait := c.min - time.Since(c.last); wait > 0 {
			time.Sleep(wait)
		}
		c.last = time.Now()
		c.Calls++
		c.mu.Unlock()

		req, err := http.NewRequestWithContext(ctx, method, apiBase+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Notion-Version", apiVersion)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(1<<attempt) * time.Second)
			continue
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			delay := time.Duration(1<<attempt) * time.Second
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if s, err := strconv.ParseFloat(ra, 64); err == nil {
					delay = time.Duration(s * float64(time.Second))
				}
			}
			lastErr = fmt.Errorf("notion %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
			time.Sleep(delay)
			continue
		}
		if resp.StatusCode >= 400 {
			e := &apiError{Status: resp.StatusCode}
			_ = json.Unmarshal(data, e)
			if e.Message == "" {
				e.Message = strings.TrimSpace(string(data))
			}
			return nil, e
		}
		return data, nil
	}
	return nil, fmt.Errorf("gave up after retries: %s %s: %w", method, path, lastErr)
}

// --- wire types -------------------------------------------------------------

type RichText struct {
	PlainText string  `json:"plain_text"`
	Href      *string `json:"href"`
}

type Named struct {
	Name string `json:"name"`
}

type DateVal struct {
	Start string  `json:"start"`
	End   *string `json:"end"`
}

type IDRef struct {
	ID string `json:"id"`
}

type Formula struct {
	Type    string   `json:"type"`
	String  *string  `json:"string"`
	Number  *float64 `json:"number"`
	Boolean *bool    `json:"boolean"`
	Date    *DateVal `json:"date"`
}

type Prop struct {
	ID             string     `json:"id"`
	Type           string     `json:"type"`
	Title          []RichText `json:"title"`
	RichText       []RichText `json:"rich_text"`
	Select         *Named     `json:"select"`
	MultiSelect    []Named    `json:"multi_select"`
	Status         *Named     `json:"status"`
	Date           *DateVal   `json:"date"`
	URL            *string    `json:"url"`
	Number         *float64   `json:"number"`
	Checkbox       *bool      `json:"checkbox"`
	Relation       []IDRef    `json:"relation"`
	HasMore        bool       `json:"has_more"`
	Formula        *Formula   `json:"formula"`
	CreatedTime    string     `json:"created_time"`
	LastEditedTime string     `json:"last_edited_time"`
}

func plain(rt []RichText) string {
	var b strings.Builder
	for _, t := range rt {
		b.WriteString(t.PlainText)
	}
	return b.String()
}

func (p *Prop) Text() string {
	if p == nil {
		return ""
	}
	switch p.Type {
	case "title":
		return plain(p.Title)
	case "rich_text":
		return plain(p.RichText)
	case "formula":
		if p.Formula != nil && p.Formula.String != nil {
			return *p.Formula.String
		}
	}
	return ""
}

func (p *Prop) Sel() string {
	if p == nil {
		return ""
	}
	if p.Select != nil {
		return p.Select.Name
	}
	if p.Status != nil {
		return p.Status.Name
	}
	return ""
}

func (p *Prop) Multi() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.MultiSelect))
	for _, m := range p.MultiSelect {
		out = append(out, m.Name)
	}
	return out
}

func (p *Prop) DateStart() string {
	if p == nil || p.Date == nil {
		return ""
	}
	return p.Date.Start
}

func (p *Prop) Str() string {
	if p == nil || p.URL == nil {
		return ""
	}
	return *p.URL
}

type Parent struct {
	Type         string `json:"type"`
	DataSourceID string `json:"data_source_id"`
	DatabaseID   string `json:"database_id"`
	PageID       string `json:"page_id"`
}

type Page struct {
	ID             string           `json:"id"`
	URL            string           `json:"url"`
	CreatedTime    string           `json:"created_time"`
	LastEditedTime string           `json:"last_edited_time"`
	Parent         Parent           `json:"parent"`
	Properties     map[string]*Prop `json:"properties"`
	InTrash        bool             `json:"in_trash"`
	Archived       bool             `json:"archived"`
}

func (pg *Page) P(name string) *Prop { return pg.Properties[name] }

func (pg *Page) Title() string {
	for _, p := range pg.Properties {
		if p.Type == "title" {
			return strings.TrimSpace(plain(p.Title))
		}
	}
	return ""
}

// --- endpoints --------------------------------------------------------------

type listResp struct {
	Results    []json.RawMessage `json:"results"`
	HasMore    bool              `json:"has_more"`
	NextCursor *string           `json:"next_cursor"`
}

func (c *Client) QueryAll(ctx context.Context, dsID string) ([]*Page, error) {
	var out []*Page
	var cursor *string
	for {
		body := map[string]any{"page_size": 100}
		if cursor != nil {
			body["start_cursor"] = *cursor
		}
		raw, err := c.do(ctx, "POST", "/data_sources/"+dsID+"/query", body)
		if err != nil {
			return nil, err
		}
		var lr listResp
		if err := json.Unmarshal(raw, &lr); err != nil {
			return nil, err
		}
		for _, r := range lr.Results {
			var pg Page
			if err := json.Unmarshal(r, &pg); err != nil {
				return nil, err
			}
			if pg.InTrash || pg.Archived {
				continue
			}
			out = append(out, &pg)
		}
		if !lr.HasMore || lr.NextCursor == nil {
			return out, nil
		}
		cursor = lr.NextCursor
	}
}

func (c *Client) GetPage(ctx context.Context, id string) (*Page, error) {
	raw, err := c.do(ctx, "GET", "/pages/"+id, nil)
	if err != nil {
		return nil, err
	}
	var pg Page
	return &pg, json.Unmarshal(raw, &pg)
}

func (c *Client) Markdown(ctx context.Context, pageID string) (string, error) {
	raw, err := c.do(ctx, "GET", "/pages/"+pageID+"/markdown", nil)
	if err != nil {
		return "", err
	}
	var r struct {
		Markdown string `json:"markdown"`
	}
	return r.Markdown, json.Unmarshal(raw, &r)
}

type ChildPage struct {
	ID, Title string
}

func (c *Client) ChildPages(ctx context.Context, pageID string) ([]ChildPage, error) {
	var out []ChildPage
	var cursor *string
	for {
		path := "/blocks/" + pageID + "/children?page_size=100"
		if cursor != nil {
			path += "&start_cursor=" + *cursor
		}
		raw, err := c.do(ctx, "GET", path, nil)
		if err != nil {
			return nil, err
		}
		var lr listResp
		if err := json.Unmarshal(raw, &lr); err != nil {
			return nil, err
		}
		for _, r := range lr.Results {
			var b struct {
				ID        string `json:"id"`
				Type      string `json:"type"`
				ChildPage *struct {
					Title string `json:"title"`
				} `json:"child_page"`
			}
			if err := json.Unmarshal(r, &b); err != nil {
				return nil, err
			}
			if b.Type == "child_page" && b.ChildPage != nil {
				out = append(out, ChildPage{ID: b.ID, Title: b.ChildPage.Title})
			}
		}
		if !lr.HasMore || lr.NextCursor == nil {
			return out, nil
		}
		cursor = lr.NextCursor
	}
}

// RelationAll fetches every related id of a relation property (query results cap at 25).
func (c *Client) RelationAll(ctx context.Context, pageID, propID string) ([]string, error) {
	var out []string
	var cursor *string
	for {
		path := "/pages/" + pageID + "/properties/" + propID + "?page_size=100"
		if cursor != nil {
			path += "&start_cursor=" + *cursor
		}
		raw, err := c.do(ctx, "GET", path, nil)
		if err != nil {
			return nil, err
		}
		var lr listResp
		if err := json.Unmarshal(raw, &lr); err != nil {
			return nil, err
		}
		for _, r := range lr.Results {
			var item struct {
				Relation IDRef `json:"relation"`
			}
			if err := json.Unmarshal(r, &item); err != nil {
				return nil, err
			}
			if item.Relation.ID != "" {
				out = append(out, item.Relation.ID)
			}
		}
		if !lr.HasMore || lr.NextCursor == nil {
			return out, nil
		}
		cursor = lr.NextCursor
	}
}

type Database struct {
	Title, Description string
}

func (c *Client) GetDatabase(ctx context.Context, id string) (Database, error) {
	raw, err := c.do(ctx, "GET", "/databases/"+id, nil)
	if err != nil {
		return Database{}, err
	}
	var d struct {
		Title       []RichText `json:"title"`
		Description []RichText `json:"description"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return Database{}, err
	}
	return Database{Title: plain(d.Title), Description: plain(d.Description)}, nil
}

// dashed turns a 32-hex Notion id into its dashed UUID form.
func dashed(id string) string {
	id = strings.ReplaceAll(strings.ToLower(id), "-", "")
	if len(id) != 32 {
		return id
	}
	return id[0:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
}
