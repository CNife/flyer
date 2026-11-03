// Package client talks to a flyer server.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/CNife/flyer/internal/api"
)

type Error struct {
	StatusCode int
	Message    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("server returned %d: %s", e.StatusCode, e.Message)
}

type Client struct {
	BaseURL  string
	Key      string
	AdminKey string
	HTTP     *http.Client
}

func New(baseURL, key, adminKey string) *Client {
	return &Client{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		Key:      key,
		AdminKey: adminKey,
		HTTP:     &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) Publish(ctx context.Context, req api.PublishRequest) (*api.PublishResponse, error) {
	var out api.PublishResponse
	if err := c.do(ctx, http.MethodPost, "/api/pages", req, &out, false); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeletePage(ctx context.Context, slug string) error {
	return c.do(ctx, http.MethodDelete, "/api/pages/"+url.PathEscape(slug), nil, nil, false)
}

func (c *Client) Stats(ctx context.Context) (*api.StatsResponse, error) {
	var out api.StatsResponse
	if err := c.do(ctx, http.MethodGet, "/api/admin/stats", nil, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Cleanup(ctx context.Context) (*api.CleanupResponse, error) {
	var out api.CleanupResponse
	if err := c.do(ctx, http.MethodPost, "/api/admin/cleanup", nil, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListKeys(ctx context.Context) ([]api.Key, error) {
	var out api.KeyListResponse
	if err := c.do(ctx, http.MethodGet, "/api/admin/keys", nil, &out, true); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

func (c *Client) CreateKey(ctx context.Context, name, role string) (*api.CreateKeyResponse, error) {
	var out api.CreateKeyResponse
	req := api.CreateKeyRequest{Name: name, Role: role}
	if err := c.do(ctx, http.MethodPost, "/api/admin/keys", req, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) RevokeKey(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/admin/keys/%d", id), nil, nil, true)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any, admin bool) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	token := c.Key
	if admin {
		token = c.AdminKey
	}
	if token == "" {
		if admin {
			return fmt.Errorf("no admin key configured")
		}
		return fmt.Errorf("no API key configured")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var apiErr api.ErrorResponse
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
			msg = apiErr.Error
		}
		return &Error{StatusCode: resp.StatusCode, Message: msg}
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return err
		}
	}
	return nil
}
