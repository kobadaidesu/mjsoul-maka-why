package cli

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"mjcap/internal/capture"
	"mjcap/internal/decode"
	"mjcap/internal/liqi"
)

func TestObservationLogsKeepRawPrivate(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	callback := nameLogger(log, nil, nil, true)
	callback(capture.Event{Seq: 1, Kind: "http_response", URL: "https://example.invalid/?token=private-token", Error: "private-player-name", BodyStatus: "body_unavailable"})
	callback(capture.Event{Seq: 2, Kind: "websocket", PayloadHex: "deadbeef", Error: "private-player-name"})
	for _, secret := range []string{"private-token", "private-player-name", "deadbeef", "https://"} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("private data logged: %s", secret)
		}
	}
	if !strings.Contains(out.String(), "seq=1") {
		t.Fatal("private event reference missing")
	}
}

func TestCaptureBindingMismatch(t *testing.T) {
	// A non-nil name inspector requests binding validation. No decoding needed.
	n := new(decode.Names)
	meta := &liqi.Metadata{GameVersion: "one", ResourceVersion: "a", SHA256: "first"}
	details, err := json.Marshal(struct {
		Liqi *liqi.Metadata `json:"liqi"`
	}{meta})
	if err != nil {
		t.Fatal(err)
	}
	e := capture.Event{Kind: "capture_context", Details: details}
	if err := checkCaptureBinding(e, n, meta); err != nil {
		t.Fatal(err)
	}
	if err := checkCaptureBinding(e, n, &liqi.Metadata{GameVersion: "two", ResourceVersion: "b", SHA256: "second"}); err == nil {
		t.Fatal("different capture version accepted")
	}
}
