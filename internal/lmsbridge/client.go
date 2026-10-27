// Package lmsbridge delegates lending decisions and records to Admin LMS.
// It never imports the simulator's customers, catalogue or ledger.
package lmsbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	base string
	http *http.Client
}
type Result struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

func NewClient(base string) (*Client, error) {
	u, e := url.Parse(base)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimRight(u.Path, "/") != "/api/v1" {
		return nil, errors.New("LMS URL must be an HTTPS /api/v1 base without credentials or query")
	}
	return &Client{strings.TrimRight(base, "/"), &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Call(ctx context.Context, method, path, token string, body []byte) (Result, error) {
	req, e := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if e != nil {
		return Result{}, e
	}
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, e := c.http.Do(req)
	if e != nil {
		return Result{}, errors.New("LMS response unavailable")
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, 2*1024*1024+1))
	if e != nil || len(b) > 2*1024*1024 || res.StatusCode >= 500 || res.StatusCode < 200 || res.StatusCode >= 300 && res.StatusCode < 400 || !json.Valid(b) {
		return Result{}, errors.New("LMS response unavailable")
	}
	return Result{res.StatusCode, b}, nil
}
func (c *Client) Identity(ctx context.Context, token string) (string, Result, error) {
	r, e := c.Call(ctx, "GET", "/users/profile", token, nil)
	if e != nil || r.Status != 200 {
		return "", r, e
	}
	var profile struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(r.Body, &profile) != nil || !strings.HasPrefix(profile.Data.ID, "user-") || !identifier.MatchString(profile.Data.ID) {
		return "", Result{}, errors.New("LMS customer identity unavailable")
	}
	return profile.Data.ID, r, nil
}
