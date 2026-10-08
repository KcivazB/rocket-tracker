package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"rocket-tracker/internal/setup"
	"rocket-tracker/internal/store"
	"rocket-tracker/internal/tracker"
)

// Client calls the agent API of a Rocket Tracker server.
type Client struct {
	Server    string // base URL
	Token     string
	UserAgent string
	HTTP      *http.Client

	mu sync.RWMutex // guards Server and Token once the client is in use
}

// SetTarget switches to another server or token (agent.json edited).
func (c *Client) SetTarget(server, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Server, c.Token = server, token
}

// ServerURL returns the current server.
func (c *Client) ServerURL() string {
	s, _ := c.target()
	return s
}

func (c *Client) target() (string, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Server, c.Token
}

// ErrUnauthorized means the device token is unknown or was revoked.
var ErrUnauthorized = errors.New("device token refused by the server (revoked?): create a new one in the dashboard and run `rltracker agent setup` again")

// RejectedError is a permanent refusal (4xx): retrying won't help.
type RejectedError struct {
	Status int
	Msg    string
}

func (e *RejectedError) Error() string { return fmt.Sprintf("rejected (%d): %s", e.Status, e.Msg) }

// NormalizeServer validates a server base URL and removes the trailing slash.
func NormalizeServer(s string) (string, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid server URL %q (expected https://host)", s)
	}
	return s, nil
}

// Report is the heartbeat body.
type Report struct {
	Version      string           `json:"version"`
	Connected    bool             `json:"connected"`
	Transport    string           `json:"transport"`
	Live         *tracker.Live    `json:"live"`
	Ini          *setup.IniStatus `json:"ini"`
	AutoIdentity *AutoIdentity    `json:"auto_identity,omitempty"`
}

// HeartbeatResponse is what the server answers to a heartbeat.
type HeartbeatResponse struct {
	User struct {
		Handle string `json:"handle"`
		Name   string `json:"name"`
	} `json:"user"`
	Device   string   `json:"device"`
	Settings Settings `json:"settings"`
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	server, token := c.target()
	req, err := http.NewRequestWithContext(ctx, method, server+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
			return &RejectedError{Status: resp.StatusCode, Msg: msg}
		}
		return fmt.Errorf("server error %d: %s", resp.StatusCode, msg)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("unexpected server answer (is %s a Rocket Tracker server?): %w", server, err)
		}
	}
	return nil
}

// Heartbeat reports the agent state and returns the player's settings.
func (c *Client) Heartbeat(ctx context.Context, r Report) (*HeartbeatResponse, error) {
	out := &HeartbeatResponse{}
	return out, c.do(ctx, http.MethodPost, "/api/agent/heartbeat", r, out)
}

// SendMatch uploads a finished match; key makes it idempotent.
func (c *Client) SendMatch(ctx context.Context, key string, m *store.Match) error {
	return c.do(ctx, http.MethodPost, "/api/agent/matches", map[string]any{"key": key, "match": m}, nil)
}

// SetManual uploads the games entered by hand for one day and mode.
func (c *Client) SetManual(ctx context.Context, d store.ManualDay) error {
	return c.do(ctx, http.MethodPut, "/api/agent/manual/"+url.PathEscape(d.Day)+"/"+url.PathEscape(d.Mode),
		map[string]int{"games": d.Games, "wins": d.Wins}, nil)
}
