package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestArgumentsWithoutNetwork(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		text string
	}{
		{nil, 0, "Usage:"},
		{[]string{"--help"}, 0, "Usage:"},
		{[]string{"fetch-proto", "--help"}, 0, "cache-dir"},
		{[]string{"capture", "--help"}, 0, "log-names"},
		{[]string{"capture", "--duration=-1s"}, 2, "invalid capture"},
		{[]string{"capture", "--liqi-meta=missing"}, 2, "supplied together"},
		{[]string{"inspect"}, 2, "Usage:"},
		{[]string{"decode"}, 2, "Usage:"},
		{[]string{"decode", "--liqi-meta=missing", "capture.jsonl"}, 2, "Usage:"},
		{[]string{"mcp", "--help"}, 0, "games-dir"},
		{[]string{"mcp", "unexpected"}, 2, "Usage:"},
		{[]string{"mcp", "--listen=bad"}, 2, "invalid listen"},
		{[]string{"mcp", "--listen=0.0.0.0:0"}, 2, "loopback"},
		{[]string{"serve"}, 2, "available"},
		{[]string{"fetch-proto", "--timeout=0"}, 2, "positive timeout"},
		{[]string{"fetch-proto", "unexpected"}, 2, "positional"},
		{[]string{"fetch-proto", "--sha256=bad"}, 1, "SHA-256"},
		{[]string{"fetch-proto", "--endpoint=https://example.invalid"}, 2, "flag provided but not defined"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out bytes.Buffer
			if code := Run(context.Background(), tc.args, &out); code != tc.code || !strings.Contains(out.String(), tc.text) {
				t.Fatalf("code=%d output=%s", code, &out)
			}
		})
	}
}
