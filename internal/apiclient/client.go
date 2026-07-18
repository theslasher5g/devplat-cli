// Package apiclient is a thin REST client for the devplat control plane's
// environment-lifecycle endpoints (POST/GET/DELETE /environments), the
// pieces of the API this CLI needs. It intentionally doesn't try to be a
// general-purpose SDK — see devplat-backend/src/routes/environments.ts for
// the server side these calls hit.
package apiclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		// POST /environments can legitimately take a while: the agent's own
		// boot-readiness wait is up to 45s per host, wrapped in a 60s agent
		// handler timeout, wrapped in the scheduler's 75s per-host HTTP
		// timeout — and it may try more than one candidate host in sequence
		// before giving up or succeeding. Two hosts worst-case approaches
		// 150s; leave real headroom so the CLI doesn't give up before the
		// server had a chance to answer even in the healthy case.
		HTTP: &http.Client{Timeout: 200 * time.Second},
	}
}

// Environment mirrors the fields the CLI actually needs from a
// GET/POST /environments(/:id) response.
type Environment struct {
	RequestID      string `json:"requestId"`
	Status         string `json:"status"` // "queued" | "assigned" | "failed" | "released"
	DockerEndpoint string `json:"dockerEndpoint"`
	Error          string `json:"error"`
}

type apiError struct {
	Error  string `json:"error"`
	Detail string `json:"detail"`
}

func (c *Client) do(method, path string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s failed: %w", path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var apiErr apiError
		_ = json.Unmarshal(raw, &apiErr)
		if apiErr.Detail != "" {
			return fmt.Errorf("%s (%s)", apiErr.Detail, apiErr.Error)
		}
		if apiErr.Error != "" {
			return fmt.Errorf("%s: %s", path, apiErr.Error)
		}
		return fmt.Errorf("%s: unexpected status %d", path, res.StatusCode)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// RequestEnvironment asks the scheduler for a new environment (microVM).
// Durable on the server side — even a "queued" result has a real requestId
// to poll, per POST /environments's contract.
func (c *Client) RequestEnvironment() (*Environment, error) {
	var env Environment
	if err := c.do(http.MethodPost, "/environments", nil, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

func (c *Client) GetEnvironment(id string) (*Environment, error) {
	var env Environment
	if err := c.do(http.MethodGet, "/environments/"+id, nil, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

func (c *Client) ReleaseEnvironment(id string) error {
	return c.do(http.MethodDelete, "/environments/"+id, nil, nil)
}
