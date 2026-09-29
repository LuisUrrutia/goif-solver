// Package transport bounds HTTP request rate, duration, and response size.
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

type Client struct {
	base     *url.URL
	http     *http.Client
	header   http.Header
	interval time.Duration
	mu       sync.Mutex
	next     time.Time
}
type StatusError struct {
	Code       int
	RetryAfter time.Duration
}

func (e *StatusError) Error() string { return fmt.Sprintf("remote HTTP status %d", e.Code) }
func New(base string, headers http.Header, requestsPerSecond int) (*Client, error) {
	u, e := url.Parse(base)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
		return nil, errors.New("invalid service URL: use HTTPS or loopback HTTP without credentials/query")
	}
	if requestsPerSecond < 1 || requestsPerSecond > 1000 {
		return nil, errors.New("request rate must be 1..1000")
	}
	requestHeaders := make(http.Header, len(headers))
	for name, values := range headers {
		requestHeaders[name] = append([]string(nil), values...)
	}
	return &Client{base: u, http: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, header: requestHeaders, interval: time.Second / time.Duration(requestsPerSecond)}, nil
}

func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	now := time.Now()
	at := c.next
	if at.Before(now) {
		at = now
	}
	c.next = at.Add(c.interval)
	c.mu.Unlock()
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Do never includes upstream bodies or URLs in errors: both can contain secrets.
// Retries belong to the caller, which knows whether an operation is idempotent.
func (c *Client) Do(ctx context.Context, method, path string, in, out interface{}) error {
	if err := c.wait(ctx); err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		b, e := json.Marshal(in)
		if e != nil {
			return errors.New("encode request")
		}
		body = bytes.NewReader(b)
	}
	relative, e := url.Parse(path)
	if e != nil || relative.IsAbs() || relative.Host != "" || relative.Fragment != "" {
		return errors.New("invalid API path")
	}
	target := c.base.ResolveReference(relative)
	req, e := http.NewRequestWithContext(ctx, method, target.String(), body)
	if e != nil {
		return errors.New("construct request")
	}
	req.Header = c.header.Clone()
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, e := c.http.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("remote request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		delay := time.Duration(0)
		if seconds, err := strconv.ParseUint(resp.Header.Get("Retry-After"), 10, 32); err == nil {
			delay = time.Duration(seconds) * time.Second
		} else if date, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil {
			delay = max(time.Until(date), 0)
		}
		return &StatusError{Code: resp.StatusCode, RetryAfter: delay}
	}
	const max = 4 << 20
	b, e := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if e != nil {
		return errors.New("read response")
	}
	if len(b) > max {
		return errors.New("response too large")
	}
	if out == nil {
		return nil
	}
	if e = json.Unmarshal(b, out); e != nil {
		return errors.New("decode response")
	}
	return nil
}
