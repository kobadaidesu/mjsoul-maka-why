package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"log/slog"
	"net"
	"strings"
	"testing"

	"mjcap/internal/capture"
)

func friendlyLogger() (*friendly, *slog.Logger, *bytes.Buffer) {
	var out bytes.Buffer
	ui := &friendly{w: &out}
	return ui, slog.New(friendlyHandler{ui}), &out
}

func wsEvent(seq uint64, direction string, payload []byte) capture.Event {
	return capture.Event{SchemaVersion: 1, Seq: seq, Kind: "websocket", ConnectionID: "c", Direction: direction, Opcode: 2, PayloadHex: hex.EncodeToString(payload)}
}

func bodyStatusEvent(seq uint64, status string) capture.Event {
	return capture.Event{SchemaVersion: 1, Seq: seq, Kind: "http_response", RequestID: "h", BodyStatus: status}
}

func TestFriendlyViewMapsKnownEvents(t *testing.T) {
	ui, logger, out := friendlyLogger()

	logger.Info("private capture opened", cliEventKey, cliEventCaptureOpened, "path", "data/captures/x.jsonl", "target_id", "T")
	logger.Info("capture ready: perform replay/MAKA actions manually", cliEventKey, cliEventCaptureReady, "path", "data/captures/x.jsonl")
	// Requests and unrelated messages stay silent; only received
	// record/report/login responses surface and count.
	logger.Info("observed message", cliEventKey, cliEventObservedMessage, "direction", "sent", "message_name", fetchGameRecordMethod)
	logger.Info("observed message", cliEventKey, cliEventObservedMessage, "direction", "received", "message_name", ".lq.Lobby.heartbeat")
	logger.Info("observed message", cliEventKey, cliEventObservedMessage, "direction", "received", "message_name", fetchGameRecordMethod)
	logger.Info("observed message", cliEventKey, cliEventObservedMessage, "direction", "received", "message_name", fetchGameRecordMethod)
	logger.Info("observed message", cliEventKey, cliEventObservedMessage, "direction", "received", "message_name", fetchSeerReportMethod)
	logger.Info("observed message", cliEventKey, cliEventObservedMessage, "direction", "received", "message_name", oauth2LoginMethod)
	logger.Warn("HTTP body observation status", cliEventKey, cliEventBodyStatus, "seq", 1, "body_status", "body_budget_limit")
	logger.Info("capture stopped", cliEventKey, cliEventCaptureStopped, "path", "data/captures/x.jsonl")
	logger.Info("game record reconstructed", cliEventKey, cliEventGameReconstructed, "rounds", 8, "decisions", 120, "maka_joined_decisions", 118,
		"maka_report", true, "self_seat_known", false, "issues", 0)
	logger.Info("self seat login reused from earlier capture", cliEventKey, cliEventLoginReused, "capture", "old.jsonl")
	logger.Info("game stored", cliEventKey, cliEventGameStored, "path", "data/games/u.json")
	logger.Warn("unmapped warning", "error", "boom")
	logger.Info("unmapped info stays hidden")

	got := out.String()
	for _, want := range []string{
		"[ok] Chrome の雀魂タブに接続",
		"[..] スキャン中",
		"[rx] 牌譜 2 件目を受信",
		"[rx] MAKA 評価 1 件目を受信",
		"[rx] ログイン応答を確認",
		"[--] スキャン終了",
		"[ok] 復元: 全8局 / 判断 120 / MAKA 結合 118",
		"[--] 自席: 不明",
		"[--] 自席判定: 過去の capture のログイン応答を再利用 (old.jsonl)",
		"[ok] 保存: data/games/u.json",
		"[!!] unmapped warning (boom)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"body_budget_limit", "heartbeat", "unmapped info"} {
		if strings.Contains(got, banned) {
			t.Errorf("output should not contain %q:\n%s", banned, got)
		}
	}
	if ui.storedGames() != 1 {
		t.Errorf("storedGames=%d want 1", ui.storedGames())
	}
}

// TestFriendlyViewKeysOnEventNotText pins the contract: rewording the
// English log text does not change the display as long as the event
// identifier stays, and legacy text alone no longer classifies a record.
func TestFriendlyViewKeysOnEventNotText(t *testing.T) {
	_, logger, out := friendlyLogger()
	logger.Info("a completely different english sentence", cliEventKey, cliEventGameStored, "path", "p.json")
	if got := out.String(); !strings.Contains(got, "[ok] 保存: p.json") {
		t.Fatalf("event with changed text lost its display:\n%s", got)
	}

	_, logger, out = friendlyLogger()
	// The exact legacy message without an event: info stays hidden, and a
	// legacy warn is shown as a generic warning, not silently suppressed
	// and not classified as a known display.
	logger.Info("game stored", "path", "p.json")
	logger.Warn("HTTP body observation status", "seq", 1, "body_status", "body_budget_limit")
	got := out.String()
	if strings.Contains(got, "[ok] 保存") {
		t.Fatalf("legacy text misclassified as a known event:\n%s", got)
	}
	if !strings.Contains(got, "[!!] HTTP body observation status") {
		t.Fatalf("event-less warning must stay visible:\n%s", got)
	}

	_, logger, out = friendlyLogger()
	logger.Error("some new failure", "error", "boom")
	logger.Warn("some new warning")
	logger.Info("some new info")
	got = out.String()
	if !strings.Contains(got, "[err] some new failure (boom)") || !strings.Contains(got, "[!!] some new warning") || strings.Contains(got, "some new info") {
		t.Fatalf("fallback levels wrong:\n%s", got)
	}
}

func TestFriendlyViewFailureHints(t *testing.T) {
	_, logger, out := friendlyLogger()
	logger.Error("no matching game record response in capture", cliEventKey, cliEventNoGameRecord)
	logger.Error("discover Chrome", cliEventKey, cliEventChromeDiscover, "error", "connection refused")
	if got := out.String(); !strings.Contains(got, "[err] 牌譜が記録されていません") ||
		!strings.Contains(got, "[err] Chrome (デバッグポート) に接続できません") ||
		strings.Contains(got, "connection refused") {
		t.Errorf("unexpected failure output:\n%s", got)
	}
}

// TestNameLoggerToFriendlyRealPath drives the production nameLogger — the
// actual emitter — into the friendly handler, so a missing cli_event
// attribute on the observed-message or body-status paths fails here.
func TestNameLoggerToFriendlyRealPath(t *testing.T) {
	n, meta, r := loginFixture(t)
	ui, logger, out := friendlyLogger()
	callback := nameLogger(logger, n, meta, true)
	req, res := loginFrames(t, r, 222222)
	callback(wsEvent(1, "sent", req))
	callback(wsEvent(2, "received", res))
	callback(wsEvent(3, "received", res)) // duplicate number: no resolved name, stays silent
	if got := out.String(); !strings.Contains(got, "[rx] ログイン応答を確認") {
		t.Fatalf("login response line missing through the real emitter:\n%s", got)
	}
	// The body-status warning is suppressed through the same real path.
	out.Reset()
	callback(bodyStatusEvent(4, "body_budget_limit"))
	if out.Len() != 0 {
		t.Fatalf("body status warning leaked through the real emitter:\n%s", out.String())
	}
	callback(bodyStatusEvent(5, "disabled"))
	if out.Len() != 0 {
		t.Fatalf("disabled status warned through the real emitter:\n%s", out.String())
	}
	_ = ui
}

// TestCaptureToFriendlyRealPath drives the real capture CLI failure path
// (loopback endpoint with nothing listening — no external connection) into
// the friendly handler, covering the discover emitter end to end.
func TestCaptureToFriendlyRealPath(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	_, logger, out := friendlyLogger()
	var errBuf bytes.Buffer
	if code := runCaptureWith(context.Background(), []string{"--endpoint", endpoint}, &errBuf, logger); code != 1 {
		t.Fatalf("capture against closed port returned %d: %s%s", code, errBuf.String(), out.String())
	}
	if got := out.String(); !strings.Contains(got, "[err] Chrome (デバッグポート) に接続できません") {
		t.Fatalf("discover failure line missing through the real emitter:\n%s", got)
	}
}

// TestDecodeToFriendlyRealPath runs the full decode CLI with the friendly
// handler installed, checking the reconstructed/stored lines appear through
// the production emitters.
func TestDecodeToFriendlyRealPath(t *testing.T) {
	metaPath, profilePath, r := offlineEvidence(t)
	dir := t.TempDir()
	capturePath := dir + "/capture.jsonl"
	writeWSCapture(t, capturePath, offlineCaptureFrames(t, r, "synthetic-uuid"))
	ui, logger, out := friendlyLogger()
	var errBuf bytes.Buffer
	if code := runDecodeWith([]string{"--liqi-meta", metaPath, "--protocol", profilePath, "--games-dir", dir + "/games", capturePath}, &errBuf, logger); code != 0 {
		t.Fatalf("decode failed (%d): %s%s", code, errBuf.String(), out.String())
	}
	got := out.String()
	if !strings.Contains(got, "[ok] 復元: 全1局 / 判断 5 / MAKA 結合 4") || !strings.Contains(got, "[ok] 保存: ") {
		t.Fatalf("friendly decode lines missing:\n%s", got)
	}
	if ui.storedGames() != 1 {
		t.Fatalf("storedGames=%d want 1", ui.storedGames())
	}
}
