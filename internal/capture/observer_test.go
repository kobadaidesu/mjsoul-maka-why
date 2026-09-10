package capture

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/network"
)

type memorySink struct {
	events  []Event
	failure error
}

func (s *memorySink) Append(e Event) (Event, error) {
	if s.failure != nil {
		return Event{}, s.failure
	}
	e.SchemaVersion = 1
	e.Seq = uint64(len(s.events) + 1)
	e.CapturedAt = time.Now().UTC()
	s.events = append(s.events, e)
	return e, nil
}

func fixturePackets(t *testing.T) []cdproto.Message {
	t.Helper()
	f, err := os.Open("../../testdata/capture/cdp.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []cdproto.Message
	s := bufio.NewScanner(f)
	for s.Scan() {
		var msg cdproto.Message
		if err := json.Unmarshal(s.Bytes(), &msg); err != nil {
			t.Fatal(err)
		}
		out = append(out, msg)
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestObserverFixture(t *testing.T) {
	sink := &memorySink{}
	calls := 0
	o := newObserver(sink, BodyPolicy{MaxBodyBytes: 1024, MaxTotalBytes: 4096}, nil, func(method string, p any) (int64, error) {
		calls++
		if method != network.CommandGetResponseBody || p.(*network.GetResponseBodyParams).RequestID != "http-a" {
			t.Fatalf("unexpected body request %s", method)
		}
		if sink.events[len(sink.events)-1].Kind != "http_finished" {
			t.Fatal("body requested before loadingFinished was saved")
		}
		return 100, nil
	})
	for _, msg := range fixturePackets(t) {
		if err := o.handle(&msg, true); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("body calls=%d", calls)
	}
	if sink.events[1].PayloadHex != "000102ff" || sink.events[1].Direction != "sent" {
		t.Fatal("binary payload changed")
	}
	if sink.events[2].PayloadHex != "73796e7468657469632074657874" {
		t.Fatal("text payload changed")
	}
	if sink.events[3].Error == "" || !bytes.Contains(sink.events[3].CDPParams, []byte("%%%")) {
		t.Fatal("invalid frame lost")
	}
	if sink.events[4].Kind != "websocket_close" {
		t.Fatal("close lost")
	}
	var body *string
	for _, e := range sink.events {
		if e.Kind == "http_response" {
			body = e.BodyBase64
		}
	}
	if body == nil {
		t.Fatal("body absent")
	}
	raw, err := base64.StdEncoding.DecodeString(*body)
	if err != nil || string(raw) != `{"synthetic":true}` {
		t.Fatalf("body changed: %s %v", raw, err)
	}
	// Double retention on the stored path: the extracted body_base64 and the
	// verbatim CDP reply result are both kept.
	packets := fixturePackets(t)
	for _, e := range sink.events {
		if e.Kind == "http_response" && e.BodyStatus == "stored" && !bytes.Equal(e.CDPResult, []byte(packets[7].Result)) {
			t.Fatalf("stored CDP result not identical: %s vs %s", e.CDPResult, packets[7].Result)
		}
	}
	if sink.events[len(sink.events)-1].CDPMethod != "Network.futureEvent" {
		t.Fatal("unknown CDP event lost")
	}
}

func TestBodyFailuresAndBudgets(t *testing.T) {
	for _, kind := range []string{"oversize", "budget", "loading_failed", "unavailable", "invalid_base64", "empty", "timeout", "stop"} {
		t.Run(kind, func(t *testing.T) {
			sink := &memorySink{}
			calls := 0
			policy := BodyPolicy{MaxBodyBytes: 1024, MaxTotalBytes: 4096}
			if kind == "oversize" {
				policy.MaxBodyBytes = 4
			}
			o := newObserver(sink, policy, nil, func(string, any) (int64, error) { calls++; return 100, nil })
			packets := fixturePackets(t)
			if err := o.handle(&packets[5], true); err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatal("early body request")
			}
			if kind == "loading_failed" {
				msg := cdproto.Message{Method: "Network.loadingFailed", Params: []byte(`{"requestId":"http-a","errorText":"synthetic"}`)}
				if err := o.handle(&msg, true); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "budget" {
				o.bodyBytes = 4096
			}
			if err := o.handle(&packets[6], kind != "stop"); err != nil {
				t.Fatal(err)
			}
			if kind == "oversize" || kind == "budget" || kind == "loading_failed" || kind == "stop" {
				if calls != 0 {
					t.Fatal("body call should be suppressed")
				}
				return
			}
			if kind == "timeout" {
				if err := o.expire(time.Now().Add(time.Minute), false); err != nil {
					t.Fatal(err)
				}
				if sink.events[len(sink.events)-1].BodyStatus != "body_timeout" {
					t.Fatal("timeout absent")
				}
				return
			}
			msg := cdproto.Message{ID: 100}
			want := "stored"
			switch kind {
			case "unavailable":
				msg.Error = &cdproto.Error{Code: -32000, Message: "synthetic failure"}
				want = "body_unavailable"
			case "invalid_base64":
				msg.Result = []byte(`{"body":"%%%","base64Encoded":true}`)
				want = "invalid_body_encoding"
			case "empty":
				msg.Result = []byte(`{"body":"","base64Encoded":false}`)
			}
			if err := o.handle(&msg, true); err != nil {
				t.Fatal(err)
			}
			last := sink.events[len(sink.events)-1]
			if last.BodyStatus != want {
				t.Fatalf("status %s want %s", last.BodyStatus, want)
			}
			if kind == "empty" && (last.BodyBase64 == nil || *last.BodyBase64 != "") {
				t.Fatal("empty body not preserved")
			}
		})
	}
}

func TestPrivateJournalReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "capture.jsonl")
	j, err := NewJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []Event{{Kind: "websocket", ConnectionID: "a", Direction: "sent", Opcode: 2, PayloadHex: "00ff"}, {Kind: "future_kind", Details: json.RawMessage(`{"raw":"retained"}`)}} {
		if _, err := j.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewJournal(path); err == nil {
		t.Fatal("existing capture overwritten")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("capture is not private")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := Replay(bytes.NewReader(data), func(e Event) error {
		count++
		if e.Seq != uint64(count) {
			t.Fatal("sequence lost")
		}
		return nil
	}); err != nil || count != 2 {
		t.Fatalf("replay: %v count %d", err, count)
	}
	for _, bad := range [][]byte{data[:len(data)-1], bytes.ReplaceAll(data, []byte(`"schema_version":1`), []byte(`"schema_version":9`)), append(append([]byte{}, data...), data...)} {
		if err := Replay(bytes.NewReader(bad), func(Event) error { return nil }); err == nil {
			t.Fatal("invalid replay accepted")
		}
	}
}

type fakeTransport struct {
	reads   chan packet
	closed  chan struct{}
	once    sync.Once
	methods []string
	mu      sync.Mutex
}

func (f *fakeTransport) Read(ctx context.Context, m *cdproto.Message) error {
	select {
	case <-f.closed:
		return io.EOF
	case p := <-f.reads:
		*m = p.message
		return p.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (f *fakeTransport) Write(_ context.Context, m *cdproto.Message) error {
	f.mu.Lock()
	f.methods = append(f.methods, string(m.Method))
	f.mu.Unlock()
	if string(m.Method) == network.CommandEnable {
		f.reads <- packet{message: cdproto.Message{ID: m.ID, Result: []byte(`{}`)}}
	}
	return nil
}
func (f *fakeTransport) Close() error { f.once.Do(func() { close(f.closed) }); return nil }

func TestSessionCancelJoinsAndDoesNotCloseBrowser(t *testing.T) {
	f := &fakeTransport{reads: make(chan packet, 8), closed: make(chan struct{})}
	sink := &memorySink{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := run(ctx, f, sink, BodyPolicy{MaxBodyBytes: 1024, MaxTotalBytes: 4096}, nil, cancel)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.methods) != 1 || f.methods[0] != network.CommandEnable {
		t.Fatalf("unexpected CDP commands: %v", f.methods)
	}
	if sink.events[len(sink.events)-1].Kind != "capture_stop" {
		t.Fatal("stop event missing")
	}
	select {
	case <-f.closed:
	default:
		t.Fatal("observation socket not closed")
	}
}

func TestReadOnlyCommandGuardAndSinkFailure(t *testing.T) {
	f := &fakeTransport{reads: make(chan packet, 8), closed: make(chan struct{})}
	for _, command := range []string{"Runtime.evaluate", "Page.navigate", "Input.dispatchMouseEvent", "Network.replayXHR", "Target.createTarget", "Browser.close"} {
		if err := sendReadOnly(context.Background(), f, 1, command, struct{}{}); err == nil {
			t.Fatalf("allowed %s", command)
		}
	}
	if len(f.methods) != 0 {
		t.Fatal("forbidden writes reached transport")
	}
	sink := &memorySink{failure: errors.New("synthetic disk failure")}
	calls := 0
	o := newObserver(sink, BodyPolicy{}, nil, func(string, any) (int64, error) { calls++; return 1, nil })
	msg := fixturePackets(t)[1]
	if err := o.handle(&msg, true); err == nil || !strings.Contains(err.Error(), "disk failure") {
		t.Fatal("sink failure swallowed")
	}
	if calls != 0 {
		t.Fatal("command after failed persistence")
	}
}

func TestReadRecordLimit(t *testing.T) {
	if _, err := readRecord(bufio.NewReader(strings.NewReader("0123456789\n")), 8); err == nil {
		t.Fatal("record limit ignored")
	}
}

// replayFixture drives one observer over the fixture packets. The stored
// body reply (ID 100) is delivered only when the observer actually issued a
// body request, mirroring a real transport where no request means no reply.
func replayFixture(t *testing.T, policy BodyPolicy) (*memorySink, *observer, int) {
	t.Helper()
	sink := &memorySink{}
	calls := 0
	o := newObserver(sink, policy, nil, func(method string, p any) (int64, error) {
		if method != network.CommandGetResponseBody {
			t.Fatalf("unexpected command %s", method)
		}
		calls++
		return 100, nil
	})
	for _, msg := range fixturePackets(t) {
		if msg.ID == 100 && calls == 0 {
			continue
		}
		if err := o.handle(&msg, true); err != nil {
			t.Fatal(err)
		}
	}
	return sink, o, calls
}

func TestDisabledHTTPBodiesNeverRequests(t *testing.T) {
	// URLPattern matches the fixture Fetch request, so this also proves the
	// disable takes precedence over both selected() and the URL pattern.
	policy := BodyPolicy{MaxBodyBytes: 1024, MaxTotalBytes: 4096, URLPattern: regexp.MustCompile("record"), DisableHTTPBodies: true}
	sink, o, calls := replayFixture(t, policy)
	if calls != 0 || len(o.responses) != 0 || len(o.pending) != 0 {
		t.Fatalf("disabled policy issued requests: calls=%d responses=%d pending=%d", calls, len(o.responses), len(o.pending))
	}
	packets := fixturePackets(t)
	var kinds []string
	for _, e := range sink.events {
		kinds = append(kinds, e.Kind)
		if e.Kind == "http_metadata" && e.BodyStatus != "disabled" && e.RequestID == "http-a" {
			t.Fatalf("selected response status %q, want disabled", e.BodyStatus)
		}
	}
	// HTTP metadata/finished raw params must be byte-identical to the
	// original CDP messages, in arrival order.
	for eventIndex, packetIndex := range map[int]int{5: 5, 6: 6, 7: 8, 8: 9} {
		if !bytes.Equal(sink.events[eventIndex].CDPParams, packets[packetIndex].Params) {
			t.Fatalf("event %d raw differs from packet %d", eventIndex, packetIndex)
		}
	}
	// Metadata and finished events stay in arrival order; no http_response
	// events exist because no bodies were ever requested.
	// Index 3 is the fixture's invalid-base64 frame, retained as cdp_event.
	want := []string{"websocket_open", "websocket", "websocket", "cdp_event", "websocket_close", "http_metadata", "http_finished", "http_metadata", "http_finished", "cdp_event"}
	if len(kinds) != len(want) {
		t.Fatalf("event kinds %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("event order %v, want %v", kinds, want)
		}
	}
}

// TestDisabledHTTPBodiesSyntheticCandidates covers candidates the fixture
// lacks: an XHR with an empty body, a Fetch over MaxBodyBytes, and a
// URLPattern match. Disabled must issue no request and track nothing for
// any of them while keeping the original params byte-for-byte.
func TestDisabledHTTPBodiesSyntheticCandidates(t *testing.T) {
	policy := BodyPolicy{MaxBodyBytes: 1024, MaxTotalBytes: 4096, URLPattern: regexp.MustCompile("selected-url"), DisableHTTPBodies: true}
	sink := &memorySink{}
	o := newObserver(sink, policy, nil, func(string, any) (int64, error) {
		t.Fatal("body requested under disabled policy")
		return 0, nil
	})
	cases := []struct{ metadata, finished string }{
		// XHR whose body would be empty (encodedDataLength=0).
		{`{"requestId":"xhr-1","type":"XHR","response":{"url":"https://example.invalid/x","status":200,"mimeType":"text/plain"}}`,
			`{"requestId":"xhr-1","timestamp":1,"encodedDataLength":0}`},
		// Fetch over MaxBodyBytes (1025 > 1024).
		{`{"requestId":"fetch-1","type":"Fetch","response":{"url":"https://example.invalid/f","status":200,"mimeType":"application/json"}}`,
			`{"requestId":"fetch-1","timestamp":2,"encodedDataLength":1025}`},
		// URLPattern match on an otherwise unselected response.
		{`{"requestId":"pat-1","type":"Other","response":{"url":"https://example.invalid/selected-url","status":200,"mimeType":"text/html"}}`,
			`{"requestId":"pat-1","timestamp":3,"encodedDataLength":512}`},
	}
	for _, tc := range cases {
		metadata := cdproto.Message{Method: "Network.responseReceived", Params: []byte(tc.metadata)}
		finished := cdproto.Message{Method: "Network.loadingFinished", Params: []byte(tc.finished)}
		if err := o.handle(&metadata, true); err != nil {
			t.Fatal(err)
		}
		if len(o.responses) != 0 || len(o.pending) != 0 {
			t.Fatalf("disabled policy tracked %s", tc.metadata)
		}
		if err := o.handle(&finished, true); err != nil {
			t.Fatal(err)
		}
		if len(o.pending) != 0 {
			t.Fatalf("disabled policy requested a body for %s", tc.finished)
		}
	}
	metadataIndex, finishedIndex := 0, 0
	for _, e := range sink.events {
		switch e.Kind {
		case "http_metadata":
			if e.BodyStatus != "disabled" || !bytes.Equal(e.CDPParams, []byte(cases[metadataIndex].metadata)) {
				t.Fatalf("metadata %d altered: %s %s", metadataIndex, e.BodyStatus, e.CDPParams)
			}
			metadataIndex++
		case "http_finished":
			if !bytes.Equal(e.CDPParams, []byte(cases[finishedIndex].finished)) {
				t.Fatalf("finished %d altered: %s", finishedIndex, e.CDPParams)
			}
			finishedIndex++
		default:
			t.Fatalf("unexpected event kind %s", e.Kind)
		}
	}
	if metadataIndex != len(cases) || finishedIndex != len(cases) {
		t.Fatalf("events lost: metadata=%d finished=%d", metadataIndex, finishedIndex)
	}
}

func TestDisabledHTTPBodiesKeepsLateRepliesAndFailures(t *testing.T) {
	sink := &memorySink{}
	o := newObserver(sink, BodyPolicy{MaxBodyBytes: 1024, MaxTotalBytes: 4096, DisableHTTPBodies: true}, nil,
		func(string, any) (int64, error) { t.Fatal("body requested"); return 0, nil })
	packets := fixturePackets(t)
	if err := o.handle(&packets[5], true); err != nil { // responseReceived http-a
		t.Fatal(err)
	}
	failed := cdproto.Message{Method: "Network.loadingFailed", Params: []byte(`{"requestId":"http-a","errorText":"synthetic"}`)}
	if err := o.handle(&failed, true); err != nil {
		t.Fatal(err)
	}
	// A reply that still arrives (late, unmatched) keeps its exact result
	// bytes; the recorded raw must equal the original message verbatim.
	if err := o.handle(&packets[7], true); err != nil {
		t.Fatal(err)
	}
	metadataEvent := sink.events[0]
	if metadataEvent.Kind != "http_metadata" || !bytes.Equal(metadataEvent.CDPParams, packets[5].Params) {
		t.Fatalf("responseReceived raw not identical: %s", metadataEvent.CDPParams)
	}
	failedEvent := sink.events[1]
	if failedEvent.Kind != "http_failed" || failedEvent.BodyStatus != "loading_failed" || !bytes.Equal(failedEvent.CDPParams, []byte(failed.Params)) {
		t.Fatal("loadingFailed raw not identical under disabled policy")
	}
	last := sink.events[2]
	if last.Kind != "cdp_reply" || !bytes.Equal(last.CDPResult, []byte(packets[7].Result)) {
		t.Fatalf("late reply raw not identical: %s vs %s", last.CDPResult, packets[7].Result)
	}
	if err := o.expire(time.Now(), true); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 3 {
		t.Fatalf("expire emitted tracked responses under disabled policy: %d events", len(sink.events))
	}
}

func TestDisabledCaptureIsSmallerWithIdenticalWebsocketRaw(t *testing.T) {
	base := BodyPolicy{MaxBodyBytes: 1024, MaxTotalBytes: 4096}
	disabledPolicy := base
	disabledPolicy.DisableHTTPBodies = true
	enabledSink, _, enabledCalls := replayFixture(t, base)
	disabledSink, _, disabledCalls := replayFixture(t, disabledPolicy)
	if enabledCalls != 1 || disabledCalls != 0 {
		t.Fatalf("calls enabled=%d disabled=%d", enabledCalls, disabledCalls)
	}
	// JSONL size: one marshaled line per event plus the trailing newline.
	size := func(events []Event) int {
		total := 0
		for _, e := range events {
			raw, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			total += len(raw) + 1
		}
		return total
	}
	stored := func(events []Event) int {
		n := 0
		for _, e := range events {
			if e.BodyBase64 != nil {
				n++
			}
		}
		return n
	}
	enabledBytes, disabledBytes := size(enabledSink.events), size(disabledSink.events)
	t.Logf("enabled:  jsonl_bytes=%d body_requests=%d bodies_stored=%d events=%d", enabledBytes, enabledCalls, stored(enabledSink.events), len(enabledSink.events))
	t.Logf("disabled: jsonl_bytes=%d body_requests=%d bodies_stored=%d events=%d", disabledBytes, disabledCalls, stored(disabledSink.events), len(disabledSink.events))
	if disabledBytes >= enabledBytes {
		t.Fatalf("disabled capture not smaller: %d vs %d", disabledBytes, enabledBytes)
	}
	ws := func(events []Event) []Event {
		var out []Event
		for _, e := range events {
			if strings.HasPrefix(e.Kind, "websocket") {
				e.Seq, e.CapturedAt = 0, time.Time{}
				out = append(out, e)
			}
		}
		return out
	}
	a, b := ws(enabledSink.events), ws(disabledSink.events)
	if len(a) != len(b) {
		t.Fatalf("websocket event counts differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		ra, _ := json.Marshal(a[i])
		rb, _ := json.Marshal(b[i])
		if !bytes.Equal(ra, rb) {
			t.Fatalf("websocket raw differs at %d: %s vs %s", i, ra, rb)
		}
	}
	if stored(enabledSink.events) != 1 || stored(disabledSink.events) != 0 {
		t.Fatalf("stored bodies enabled=%d disabled=%d", stored(enabledSink.events), stored(disabledSink.events))
	}
}

func TestBinaryBodyOvershootPreservesAllBytes(t *testing.T) {
	sink := &memorySink{}
	o := newObserver(sink, BodyPolicy{MaxBodyBytes: 2, MaxTotalBytes: 4}, nil, nil)
	msg := cdproto.Message{Result: []byte(`{"body":"AAEC/w==","base64Encoded":true}`)}
	if err := o.body(Event{RequestID: "a"}, &msg); err != nil {
		t.Fatal(err)
	}
	last := sink.events[0]
	if last.BodyStatus != "stored_over_limit" || last.BodyBase64 == nil || *last.BodyBase64 != "AAEC/w==" {
		t.Fatal("overshoot bytes discarded")
	}
	// Double retention holds on the overshoot path too: the verbatim CDP
	// reply result is kept alongside the extracted body.
	if !bytes.Equal(last.CDPResult, []byte(msg.Result)) {
		t.Fatalf("overshoot CDP result not identical: %s", last.CDPResult)
	}
}
