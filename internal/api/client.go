package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/dncore/wg-service/internal/logs"
	"github.com/dncore/wg-service/internal/wire"
)

// Client talks to the daemon over its unix socket.
type Client struct {
	hc   *http.Client
	base string
}

// Connect builds a client for the daemon socket.
func Connect(sockPath string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sockPath)
		},
	}
	return &Client{
		hc:   &http.Client{Transport: tr, Timeout: 30 * time.Second},
		base: "http://unix",
	}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(data)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("%s", e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// State returns daemon info.
func (c *Client) State(ctx context.Context) (*wire.StateInfo, error) {
	var st wire.StateInfo
	if err := c.do(ctx, "GET", "/state", nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Instances returns the snapshot of all instances.
func (c *Client) Instances(ctx context.Context) ([]wire.InstanceView, error) {
	var v []wire.InstanceView
	if err := c.do(ctx, "GET", "/instances", nil, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// Status returns the live status of one instance.
func (c *Client) Status(ctx context.Context, name string) (*wire.StatusResp, error) {
	var s wire.StatusResp
	if err := c.do(ctx, "GET", "/instances/"+name+"/status", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Conf returns the serialized config of one instance.
func (c *Client) Conf(ctx context.Context, name string) (string, error) {
	var m struct {
		Content string `json:"content"`
	}
	if err := c.do(ctx, "GET", "/instances/"+name+"/conf", nil, &m); err != nil {
		return "", err
	}
	return m.Content, nil
}

// Create makes a new instance.
func (c *Client) Create(ctx context.Context, name, content string, force bool) error {
	return c.do(ctx, "POST", "/instances", map[string]any{"name": name, "content": content, "force": force}, nil)
}

// Update replaces the content of an existing instance.
func (c *Client) Update(ctx context.Context, name, content string, force bool) error {
	return c.do(ctx, "PUT", "/instances/"+name, map[string]any{"content": content, "force": force}, nil)
}

// Delete removes an instance.
func (c *Client) Delete(ctx context.Context, name string, force bool) error {
	return c.do(ctx, "DELETE", "/instances/"+name+"?force="+b2s(force), nil, nil)
}

// Up starts an instance.
func (c *Client) Up(ctx context.Context, name string) error {
	return c.do(ctx, "POST", "/instances/"+name+"/up", nil, nil)
}

// Down stops an instance.
func (c *Client) Down(ctx context.Context, name string) error {
	return c.do(ctx, "POST", "/instances/"+name+"/down", nil, nil)
}

// Restart bounces an instance.
func (c *Client) Restart(ctx context.Context, name string) error {
	return c.do(ctx, "POST", "/instances/"+name+"/restart", nil, nil)
}

// SetEnabled toggles boot autostart.
func (c *Client) SetEnabled(ctx context.Context, name string, enabled bool) error {
	return c.do(ctx, "PATCH", "/instances/"+name, map[string]any{"enabled": enabled}, nil)
}

// LogsQuery fetches history matching the filter.
func (c *Client) LogsQuery(ctx context.Context, f logs.Filter) ([]logs.Event, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/logs"+logQuery(f), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []logs.Event
	sc := json.NewDecoder(resp.Body)
	for {
		var e logs.Event
		if err := sc.Decode(&e); err != nil {
			break
		}
		out = append(out, e)
	}
	return out, nil
}

// LogsFollow streams live events matching the filter; fn is called per event
// until the context is canceled or the stream breaks.
func (c *Client) LogsFollow(ctx context.Context, f logs.Filter, fn func(logs.Event) error) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/logs"+logQuery(f)+"&follow=1", nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("follow: %s", resp.Status)
	}
	sc := json.NewDecoder(resp.Body)
	for {
		var e logs.Event
		if err := sc.Decode(&e); err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
}

func logQuery(f logs.Filter) string {
	q := "?"
	if f.Instance != "" {
		q += "instance=" + f.Instance + "&"
	}
	if f.MinLevel > logs.Debug {
		q += "level=" + f.MinLevel.String() + "&"
	}
	if f.Text != "" {
		q += "q=" + f.Text + "&"
	}
	if f.Limit > 0 {
		q += fmt.Sprintf("limit=%d&", f.Limit)
	}
	if !f.Since.IsZero() {
		q += "since=" + f.Since.Format(time.RFC3339) + "&"
	}
	return q
}

func b2s(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
