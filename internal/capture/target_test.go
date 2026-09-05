package capture

import "testing"

func TestEndpointAndTargetValidation(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:9222", "http://localhost:9222", "http://[::1]:9222"} {
		if _, err := endpoint(raw, false); err != nil {
			t.Fatalf("local rejected: %s %v", raw, err)
		}
	}
	for _, raw := range []string{"http://example.invalid:9222", "http://192.168.0.2:9222", "http://localhost.evil:9222", "http://user:secret@localhost:9222", "http://localhost:9222/api", "http://localhost:9222/?token=secret", "file:///tmp/cdp"} {
		if _, err := endpoint(raw, false); err == nil {
			t.Fatalf("unsafe accepted: %s", raw)
		}
	}
	if _, err := endpoint("https://example.invalid:9222", true); err != nil {
		t.Fatal(err)
	}
	target := Target{ID: "abc", Type: "page", URL: "https://example.invalid/replay?private=synthetic", WebSocketDebuggerURL: "ws://localhost:9222/devtools/page/abc"}
	if _, err := SelectTarget([]Target{target}, "", "example.invalid", "http://127.0.0.1:9222", false); err != nil {
		t.Fatal(err)
	}
	if _, err := SelectTarget([]Target{target, target}, "", "example.invalid", "http://127.0.0.1:9222", false); err == nil {
		t.Fatal("ambiguous target accepted")
	}
	for _, ws := range []string{"ws://example.invalid:9222/devtools/page/abc", "ws://localhost:9223/devtools/page/abc", "ws://localhost:9222/devtools/browser/abc", "ws://localhost:9222/devtools/page/abc?secret=x"} {
		target.WebSocketDebuggerURL = ws
		if _, err := SelectTarget([]Target{target}, "abc", "", "http://127.0.0.1:9222", false); err == nil {
			t.Fatalf("unsafe socket accepted %s", ws)
		}
	}
}
