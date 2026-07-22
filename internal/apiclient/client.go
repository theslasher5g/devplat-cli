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
		// boot-readiness wait is up to 30s per host, wrapped in a 90s agent
		// handler timeout, wrapped in the scheduler's 110s per-host HTTP
		// timeout — and it may try more than one candidate host in sequence
		// before giving up or succeeding. Two hosts worst-case approaches
		// 220s; leave real headroom so the CLI doesn't give up before the
		// server had a chance to answer even in the healthy case.
		HTTP: &http.Client{Timeout: 260 * time.Second},
	}
}

// Environment mirrors the fields the CLI actually needs from a
// GET/POST /environments(/:id) response. The richer fields (vcpu/ram/ttl/
// region/usage) are only populated by GET /environments/:id, not POST.
type Environment struct {
	RequestID      string `json:"requestId"`
	Status         string `json:"status"` // "queued" | "assigned" | "failed" | "released"
	DockerEndpoint string `json:"dockerEndpoint"`
	Error          string `json:"error"`

	Vcpu       int64  `json:"vcpu"`
	RamMb      int64  `json:"ramMb"`
	Region     string `json:"region"`
	HostName   string `json:"hostName"`
	ExpiresAt  string `json:"expiresAt"` // RFC3339, or "" before assignment
	TTLMinutes int    `json:"ttlMinutes"`
	Usage      struct {
		Running int `json:"running"`
		Limit   int `json:"limit"`
	} `json:"usage"`
}

// PlatformStatus is the slice of GET /status the TUI header shows.
type PlatformStatus struct {
	Overall struct {
		Status string `json:"status"`
		Label  string `json:"label"`
	} `json:"overall"`
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

// PlatformStatus fetches the public status summary for the TUI header. Returns
// a zero value (and no error) treated by callers as "unknown" on failure.
func (c *Client) PlatformStatus() (*PlatformStatus, error) {
	var s PlatformStatus
	if err := c.do(http.MethodGet, "/status", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// RevokeToken invalidates the token this client authenticates with, server-
// side (DELETE /auth/token). Used by `devplat logout` so a logged-out machine
// can't keep starting environments. Idempotent on the server.
func (c *Client) RevokeToken() error {
	return c.do(http.MethodDelete, "/auth/token", nil, nil)
}

// --- Device-authorization flow (`devplat login`) ---
//
// These two endpoints are unauthenticated (there's no token yet — that's the
// whole point), so they bypass c.do's Bearer header via doNoAuth.

// DeviceAuth is the response to POST /auth/device/start.
type DeviceAuth struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

// DeviceToken is the response to POST /auth/device/token. Status is one of
// "pending", "denied", or "complete"; Token/APIURL are set only when complete.
type DeviceToken struct {
	Status string `json:"status"`
	Token  string `json:"token"`
	APIURL string `json:"apiUrl"`
}

func (c *Client) StartDeviceAuth() (*DeviceAuth, error) {
	var out DeviceAuth
	if err := c.doNoAuth(http.MethodPost, "/auth/device/start", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PollDeviceToken makes one poll. A "pending" status is returned as a normal
// result (not an error), so the caller can keep polling; transport/HTTP errors
// are returned as errors.
func (c *Client) PollDeviceToken(deviceCode string) (*DeviceToken, error) {
	var out DeviceToken
	if err := c.doNoAuth(http.MethodPost, "/auth/device/token", map[string]string{"deviceCode": deviceCode}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// doNoAuth is c.do without the Authorization header, for the pre-login
// device-flow endpoints. Kept separate rather than conditionally omitting the
// header in do() so the authenticated path stays obviously always-authed.
func (c *Client) doNoAuth(method, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(method, c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
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
		if apiErr.Error != "" {
			return fmt.Errorf("%s", apiErr.Error)
		}
		return fmt.Errorf("%s: unexpected status %d", path, res.StatusCode)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}
