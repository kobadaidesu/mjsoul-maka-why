package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"mjcap/internal/capture"
)

func TestCaptureBodyPolicyFlagMapping(t *testing.T) {
	// capture's default (--http-bodies=true) keeps bodies enabled.
	p, err := captureBodyPolicy(32<<20, 128<<20, "", true)
	if err != nil || p.DisableHTTPBodies || p.MaxBodyBytes != 32<<20 || p.MaxTotalBytes != 128<<20 {
		t.Fatalf("enabled mapping: %+v %v", p, err)
	}
	// ingest's default (--http-bodies=false) becomes the opt-out.
	p, err = captureBodyPolicy(32<<20, 128<<20, "record", false)
	if err != nil || !p.DisableHTTPBodies || p.URLPattern == nil || !p.URLPattern.MatchString("record") {
		t.Fatalf("disabled mapping: %+v %v", p, err)
	}
	for _, bad := range [][2]int64{{0, 4096}, {1024, 512}, {1024, 2 << 30}} {
		if _, err := captureBodyPolicy(bad[0], bad[1], "", true); err == nil {
			t.Fatalf("budgets %v accepted", bad)
		}
	}
	if _, err := captureBodyPolicy(1024, 4096, "(", true); err == nil {
		t.Fatal("invalid regexp accepted")
	}
}

func TestIngestCaptureArgsAlwaysPassHTTPBodies(t *testing.T) {
	has := func(args []string, want string) bool {
		for _, a := range args {
			if a == want {
				return true
			}
		}
		return false
	}
	friendly := ingestCaptureArgs("http://127.0.0.1:9222", "cap.jsonl", "m.json", "p.json", 0, true, false)
	plain := ingestCaptureArgs("http://127.0.0.1:9222", "cap.jsonl", "m.json", "p.json", time.Second, false, false)
	optIn := ingestCaptureArgs("http://127.0.0.1:9222", "cap.jsonl", "m.json", "p.json", 0, true, true)
	if !has(friendly, "--http-bodies=false") || !has(plain, "--http-bodies=false") {
		t.Fatalf("default off missing: friendly=%v plain=%v", friendly, plain)
	}
	if !has(optIn, "--http-bodies=true") {
		t.Fatalf("opt-in missing: %v", optIn)
	}
	if !has(friendly, "--log-names") || has(plain, "--log-names") {
		t.Fatalf("log-names wiring changed: friendly=%v plain=%v", friendly, plain)
	}
	if !has(plain, "--duration") {
		t.Fatalf("duration lost: %v", plain)
	}
}

func TestEncodeCaptureContextRecordsHTTPBodies(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		raw, err := encodeCaptureContext(capture.Target{}, nil, "", nil, 1024, 4096, "", enabled)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			HTTPBodies *bool `json:"http_bodies"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		// false must be written too, so a body-less capture is
		// distinguishable from a pre-flag capture with no field at all.
		if got.HTTPBodies == nil || *got.HTTPBodies != enabled {
			t.Fatalf("http_bodies=%v want %v in %s", got.HTTPBodies, enabled, raw)
		}
	}
	// Old contexts without the field still bind cleanly.
	n, meta, _ := loginFixture(t)
	ctx, err := json.Marshal(struct {
		Liqi any `json:"liqi"`
	}{meta})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkCaptureBinding(capture.Event{Kind: "capture_context", Details: ctx}, n, meta); err != nil {
		t.Fatalf("old context rejected: %v", err)
	}
}

// TestCaptureFlagParsePolicyBoundary drives the real capture flag parsing,
// so a broken default or dropped flag fails here even if the helper is fine.
func TestCaptureFlagParsePolicyBoundary(t *testing.T) {
	var out bytes.Buffer
	opts, code := parseCaptureFlags(nil, &out)
	if code != 0 || opts == nil || opts.policy.DisableHTTPBodies {
		t.Fatalf("capture default must keep bodies enabled: %+v code=%d", opts, code)
	}
	if opts.policy.MaxBodyBytes != 32<<20 || opts.policy.MaxTotalBytes != 128<<20 {
		t.Fatalf("default budgets changed: %+v", opts.policy)
	}
	opts, code = parseCaptureFlags([]string{"--http-bodies=false"}, &out)
	if code != 0 || opts == nil || !opts.policy.DisableHTTPBodies {
		t.Fatalf("--http-bodies=false not mapped: %+v code=%d", opts, code)
	}
	opts, code = parseCaptureFlags([]string{"--http-bodies=true", "--body-url-regexp", "record"}, &out)
	if code != 0 || opts == nil || opts.policy.DisableHTTPBodies || opts.policy.URLPattern == nil {
		t.Fatalf("explicit true not mapped: %+v code=%d", opts, code)
	}
	if opts, code = parseCaptureFlags([]string{"--max-body-bytes=0"}, &out); opts != nil || code != 2 {
		t.Fatal("invalid budgets accepted by flag parse")
	}
}

// TestIngestHandsHTTPBodiesToCapture runs the real ingest CLI entry and the
// real capture flag parsing on the exact argv ingest produced, so a break
// anywhere along runIngest -> args -> capture flags -> BodyPolicy fails.
func TestIngestHandsHTTPBodiesToCapture(t *testing.T) {
	var captured []string
	orig := runCaptureFn
	runCaptureFn = func(_ context.Context, args []string, _ io.Writer, _ *slog.Logger) int {
		captured = args
		return 3 // sentinel: stop ingest before decode
	}
	defer func() { runCaptureFn = orig }()
	for _, tc := range []struct {
		args    []string
		disable bool
	}{
		{[]string{"ingest", "--liqi-meta", "m.json", "--protocol", "p.json"}, true},
		{[]string{"ingest", "--plain", "--liqi-meta", "m.json", "--protocol", "p.json"}, true},
		{[]string{"ingest", "--http-bodies", "--liqi-meta", "m.json", "--protocol", "p.json"}, false},
	} {
		captured = nil
		var out bytes.Buffer
		if code := Run(context.Background(), tc.args, &out); code != 3 {
			t.Fatalf("%v: ingest did not reach capture (code %d, output %s)", tc.args, code, out.String())
		}
		opts, code := parseCaptureFlags(captured, &out)
		if code != 0 || opts == nil {
			t.Fatalf("%v: capture rejected ingest argv %v", tc.args, captured)
		}
		if opts.policy.DisableHTTPBodies != tc.disable {
			t.Fatalf("%v: DisableHTTPBodies=%v want %v (argv %v)", tc.args, opts.policy.DisableHTTPBodies, tc.disable, captured)
		}
	}
}

func TestNameLoggerTreatsDisabledAsNormal(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, nil))
	callback := nameLogger(logger, nil, nil, false)
	callback(capture.Event{Kind: "http_metadata", Seq: 1, BodyStatus: "disabled"})
	if out.Len() != 0 {
		t.Fatalf("disabled status warned: %s", out.String())
	}
	callback(capture.Event{Kind: "http_response", Seq: 2, BodyStatus: "body_budget_limit"})
	if !strings.Contains(out.String(), "body_budget_limit") {
		t.Fatalf("real body problem not surfaced: %s", out.String())
	}
}
