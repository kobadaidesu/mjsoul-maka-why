package capture

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type Target struct {
	validated            bool
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func endpoint(raw string, remote bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, fmt.Errorf("invalid CDP endpoint (credentials/query/fragment are forbidden)")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("CDP discovery endpoint must be http or https")
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("CDP discovery endpoint must not contain a path")
	}
	if strings.EqualFold(u.Hostname(), "localhost") {
		// Canonicalize without DNS; default local access cannot be DNS-rebound.
		host := "127.0.0.1"
		if u.Port() != "" {
			host += ":" + u.Port()
		}
		u.Host = host
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if !remote && (err != nil || !addr.IsLoopback()) {
		return nil, fmt.Errorf("non-loopback CDP endpoint requires --allow-remote-cdp")
	}
	u.Path = ""
	return u, nil
}

// Discover only reads Chrome's target list; it never creates or navigates tabs.
func Discover(ctx context.Context, raw string, remote bool) ([]Target, error) {
	u, err := endpoint(raw, remote)
	if err != nil {
		return nil, err
	}
	u.Path = "/json/list"
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("CDP discovery redirects are forbidden")
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("CDP discovery request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CDP discovery endpoint=%s attempts=1 status=unavailable: %w", u.Host, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	closeErr := resp.Body.Close()
	if resp.StatusCode != http.StatusOK || readErr != nil || closeErr != nil || len(data) > 2<<20 {
		return nil, fmt.Errorf("CDP discovery endpoint=%s attempts=1 status=%d: invalid/oversized response or read failure", u.Host, resp.StatusCode)
	}
	var targets []Target
	if err := json.Unmarshal(data, &targets); err != nil {
		return nil, fmt.Errorf("decode CDP target list: %w", err)
	}
	return targets, nil
}

// SelectTarget checks the actual socket destination independently of discovery.
// host is chosen by the CLI/user, not inferred from game protocol messages.
func SelectTarget(targets []Target, id, host, base string, remote bool) (Target, error) {
	u, err := endpoint(base, remote)
	if err != nil {
		return Target{}, err
	}
	var matches []Target
	for _, t := range targets {
		page, err := url.Parse(t.URL)
		if t.Type != "page" || err != nil {
			continue
		}
		if (id != "" && t.ID == id) || (id == "" && page.Hostname() == host) {
			matches = append(matches, t)
		}
	}
	if len(matches) != 1 {
		return Target{}, fmt.Errorf("expected one existing page target, found %d; use --target-id for an explicit tab", len(matches))
	}
	t := matches[0]
	ws, err := url.Parse(t.WebSocketDebuggerURL)
	if err != nil || ws.User != nil || ws.RawQuery != "" || ws.Fragment != "" || !strings.HasPrefix(ws.Path, "/devtools/page/") || t.ID == "" {
		return Target{}, fmt.Errorf("invalid page debugger socket")
	}
	wantScheme := "ws"
	if u.Scheme == "https" {
		wantScheme = "wss"
	}
	if strings.EqualFold(ws.Hostname(), "localhost") {
		ws.Host = strings.Replace(ws.Host, ws.Hostname(), "127.0.0.1", 1)
	}
	if ws.Scheme != wantScheme || ws.Host != u.Host {
		return Target{}, fmt.Errorf("debugger socket must match the approved CDP host/port and security scheme")
	}
	t.WebSocketDebuggerURL = ws.String()
	t.validated = true
	return t, nil
}
