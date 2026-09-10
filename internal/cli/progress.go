package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// friendly reformats the structured ingest logs into short status lines for
// people watching the terminal. It only rewords records the existing loggers
// already emit (message names, counts, stored paths); payloads, URLs and
// player data never reach a logger in the first place, so the privacy rules
// are unchanged.
type friendly struct {
	mu      sync.Mutex
	w       io.Writer
	records int
	reports int
	stored  int
}

func (f *friendly) print(format string, args ...any) {
	fmt.Fprintf(f.w, format+"\n", args...)
}

func (f *friendly) storedGames() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stored
}

func (f *friendly) handle(level slog.Level, msg string, attrs map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch msg {
	// Per-request body bookkeeping (e.g. body_budget_limit from game assets)
	// stays in the private capture; it does not affect the WebSocket record.
	case "HTTP body observation status", "observation detail retained in private capture":
		return
	case "observed message":
		if attrs["direction"] != "received" {
			return
		}
		switch attrs["message_name"] {
		case fetchGameRecordMethod:
			f.records++
			f.print("[rx] 牌譜 %d 件目を受信", f.records)
		case fetchSeerReportMethod:
			f.reports++
			f.print("[rx] MAKA 評価 %d 件目を受信", f.reports)
		case oauth2LoginMethod:
			f.print("[rx] ログイン応答を確認 (自席判定に使用)")
		}
		return
	case "private capture opened":
		f.print("[ok] Chrome の雀魂タブに接続")
		return
	case "capture ready: perform replay/MAKA actions manually":
		f.print("[..] スキャン中")
		f.print("     牌譜を開いて MAKA を表示してください (複数件可)")
		f.print("     終了するには Ctrl-C")
		return
	case "capture stopped":
		f.print("[--] スキャン終了、解析を開始")
		return
	case "game record reconstructed":
		f.print("[ok] 復元: 全%s局 / 判断 %s / MAKA 結合 %s",
			attrs["rounds"], attrs["decisions"], attrs["maka_joined_decisions"])
		if attrs["maka_report"] == "false" {
			f.print("[!!] MAKA 評価が未取得 (MAKA 未表示の可能性)")
		}
		if attrs["self_seat_known"] == "false" {
			f.print("[--] 自席: 不明 (スキャン中にログイン通信がない場合は判定不可)")
		}
		if attrs["issues"] != "" && attrs["issues"] != "0" {
			f.print("[!!] 復元時の注意 %s 件 (保存 JSON の issues を参照)", attrs["issues"])
		}
		return
	case "game stored":
		f.stored++
		f.print("[ok] 保存: %s", attrs["path"])
		return
	case "no matching game record response in capture":
		f.print("[err] 牌譜が記録されていません")
		f.print("      対処: 牌譜を開いて MAKA を表示してから Ctrl-C してください")
		return
	case "discover Chrome":
		f.print("[err] Chrome (デバッグポート) に接続できません")
		f.print("      対処: maka コマンドで専用 Chrome を起動してから再実行してください")
		return
	case "select existing tab":
		f.print("[err] Chrome に雀魂のタブが見つかりません")
		f.print("      対処: https://game.mahjongsoul.com/ を開いてから再実行してください")
		return
	case "capture interrupted; saved raw retained":
		f.print("[!!] スキャン中断 (記録済みデータは保持: %s)", attrs["path"])
		return
	}
	// Unmapped warnings/errors still surface so nothing fails silently;
	// unmapped info-level chatter stays hidden.
	switch {
	case level >= slog.LevelError:
		f.print("[err] %s%s", msg, friendlyDetail(attrs))
	case level >= slog.LevelWarn:
		f.print("[!!] %s%s", msg, friendlyDetail(attrs))
	}
}

func friendlyDetail(attrs map[string]string) string {
	if e := attrs["error"]; e != "" {
		return " (" + e + ")"
	}
	return ""
}

// friendlyHandler adapts friendly to slog so the capture/decode wiring keeps
// logging through the standard logger it already uses.
type friendlyHandler struct{ f *friendly }

func (h friendlyHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= slog.LevelInfo
}

func (h friendlyHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]string, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	h.f.handle(r.Level, r.Message, attrs)
	return nil
}

func (h friendlyHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h friendlyHandler) WithGroup(string) slog.Handler { return h }
