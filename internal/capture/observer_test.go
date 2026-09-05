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
}
