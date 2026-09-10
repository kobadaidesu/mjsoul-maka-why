package cli

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestFriendlyViewMapsKnownRecords(t *testing.T) {
	var out bytes.Buffer
	ui := &friendly{w: &out}
	logger := slog.New(friendlyHandler{ui})

	logger.Info("private capture opened", "path", "data/captures/x.jsonl", "target_id", "T")
	logger.Info("capture ready: perform replay/MAKA actions manually", "path", "data/captures/x.jsonl")
	// Requests and unrelated messages stay silent; only received
	// record/report/login responses surface.
	logger.Info("observed message", "direction", "sent", "message_name", fetchGameRecordMethod)
	logger.Info("observed message", "direction", "received", "message_name", ".lq.Lobby.heartbeat")
	logger.Info("observed message", "direction", "received", "message_name", fetchGameRecordMethod)
	logger.Info("observed message", "direction", "received", "message_name", fetchGameRecordMethod)
	logger.Info("observed message", "direction", "received", "message_name", fetchSeerReportMethod)
	logger.Info("observed message", "direction", "received", "message_name", oauth2LoginMethod)
	logger.Warn("HTTP body observation status", "seq", 1, "body_status", "body_budget_limit")
	logger.Info("capture stopped", "path", "data/captures/x.jsonl")
	logger.Info("game record reconstructed", "rounds", 8, "decisions", 120, "maka_joined_decisions", 118,
		"maka_report", true, "self_seat_known", false, "issues", 0)
	logger.Info("game stored", "path", "data/games/u.json")
	logger.Warn("unmapped warning", "error", "boom")
	logger.Info("unmapped info stays hidden")

	got := out.String()
	for _, want := range []string{
		"接続しました",
		"スキャン中",
		"牌譜をキャッチ！（2件目）",
		"MAKA 評価をキャッチ！（1件目）",
		"ログインを確認しました",
		"スキャン終了",
		"全8局・判断120回（MAKA 結合 118回）",
		"自分の席は特定できませんでした",
		"💾 保存しました → data/games/u.json",
		"⚠️ unmapped warning（boom）",
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

func TestFriendlyViewFailureHints(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(friendlyHandler{&friendly{w: &out}})
	logger.Error("no matching game record response in capture")
	logger.Error("discover Chrome", "error", "connection refused")
	if got := out.String(); !strings.Contains(got, "牌譜が 1 件も記録されていません") ||
		!strings.Contains(got, "Chrome に接続できませんでした") ||
		strings.Contains(got, "connection refused") {
		t.Errorf("unexpected failure output:\n%s", got)
	}
}
