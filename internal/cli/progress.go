package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// friendly reformats the structured ingest logs into short Japanese progress
// lines for people watching the terminal. It only rewords records the
// existing loggers already emit (message names, counts, stored paths);
// payloads, URLs and player data never reach a logger in the first place, so
// the privacy rules are unchanged.
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
	// stays in the private capture; it would only scare people here.
	case "HTTP body observation status", "observation detail retained in private capture":
		return
	case "observed message":
		if attrs["direction"] != "received" {
			return
		}
		switch attrs["message_name"] {
		case fetchGameRecordMethod:
			f.records++
			f.print("   🀄 牌譜をキャッチ！（%d件目）", f.records)
		case fetchSeerReportMethod:
			f.reports++
			f.print("   🤖 MAKA 評価をキャッチ！（%d件目）", f.reports)
		case oauth2LoginMethod:
			f.print("   🔑 ログインを確認しました（自分の席を判定できます）")
		}
		return
	case "private capture opened":
		f.print("🔌 Chrome の雀魂タブに接続しました")
		return
	case "capture ready: perform replay/MAKA actions manually":
		f.print("📡 スキャン中…")
		f.print("   👉 Chrome の雀魂で牌譜を開いて、MAKA（AI 評価）を表示してね")
		f.print("   👉 何件でも続けて OK。終わったらこのターミナルで Ctrl-C！")
		return
	case "capture stopped":
		f.print("")
		f.print("⏹  スキャン終了。解析を始めます…")
		return
	case "game record reconstructed":
		f.print("🔍 牌譜を復元しました：全%s局・判断%s回（MAKA 結合 %s回）",
			attrs["rounds"], attrs["decisions"], attrs["maka_joined_decisions"])
		if attrs["maka_report"] == "false" {
			f.print("   ⚠️ この牌譜に MAKA 評価が付いていません（MAKA 表示を忘れたかも）")
		}
		if attrs["self_seat_known"] == "false" {
			f.print("   ℹ️ 自分の席は特定できませんでした（このスキャン中にログインが無いと不明になります）")
		}
		if attrs["issues"] != "" && attrs["issues"] != "0" {
			f.print("   ⚠️ 復元時の注意点が %s 件あります（保存 JSON の issues を参照）", attrs["issues"])
		}
		return
	case "game stored":
		f.stored++
		f.print("💾 保存しました → %s", attrs["path"])
		return
	case "no matching game record response in capture":
		f.print("😢 牌譜が 1 件も記録されていませんでした")
		f.print("   💡 Chrome で牌譜を開いて MAKA を表示してから Ctrl-C してね")
		return
	case "discover Chrome":
		f.print("❌ Chrome に接続できませんでした")
		f.print("   💡 `maka` コマンドで専用 Chrome（ポート 9222）を起動してから再実行してね")
		return
	case "select existing tab":
		f.print("❌ Chrome に雀魂のタブが見つかりませんでした")
		f.print("   💡 https://game.mahjongsoul.com/ を開いてから再実行してね")
		return
	case "capture interrupted; saved raw retained":
		f.print("⚠️ スキャンが中断されました（記録済みデータは残っています: %s）", attrs["path"])
		return
	}
	// Unmapped warnings/errors still surface so nothing fails silently;
	// unmapped info-level chatter stays hidden.
	switch {
	case level >= slog.LevelError:
		f.print("❌ %s%s", msg, friendlyDetail(attrs))
	case level >= slog.LevelWarn:
		f.print("⚠️ %s%s", msg, friendlyDetail(attrs))
	}
}

func friendlyDetail(attrs map[string]string) string {
	if e := attrs["error"]; e != "" {
		return "（" + e + "）"
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
