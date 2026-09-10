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
		"[ok] Chrome の雀魂タブに接続",
		"[..] スキャン中",
		"[rx] 牌譜 2 件目を受信",
		"[rx] MAKA 評価 1 件目を受信",
		"[rx] ログイン応答を確認",
		"[--] スキャン終了",
		"[ok] 復元: 全8局 / 判断 120 / MAKA 結合 118",
		"[--] 自席: 不明",
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

func TestFriendlyViewFailureHints(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(friendlyHandler{&friendly{w: &out}})
	logger.Error("no matching game record response in capture")
	logger.Error("discover Chrome", "error", "connection refused")
	if got := out.String(); !strings.Contains(got, "[err] 牌譜が記録されていません") ||
		!strings.Contains(got, "[err] Chrome (デバッグポート) に接続できません") ||
		strings.Contains(got, "connection refused") {
		t.Errorf("unexpected failure output:\n%s", got)
	}
}
