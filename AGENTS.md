# Project rules

このプロジェクトはユーザー提示の「雀魂 MAKA 牌譜キャプチャ → MCP サーバー Project Constitution」に従う。

- Phase 0 は完了。ユーザーの「phase進めて」により Phase 1 の実装・実測が承認された。Phase 2 以降は未承認。Phase 0→1→2→3→4 の各境界でユーザー承認が必要。受け入れ条件未達で完了扱いしない。
- ゲーム通信は観測専用。独自 WebSocket 送信、request replay、ゲーム API 呼び出し、MAKA 分析要求、ブラウザ自動操作、data_url の独自取得は禁止。
- HTTP GET は protobuf 定義取得に必要な公開静的リソースに限定する。
- プロトコルの message / URL / field / number / enum / score 意味を推測で実装しない。CONFIRMED / CANDIDATE / UNKNOWN を区別し、実測は docs/protocol-findings.md に日時、各 version、方向、transport、操作、方法とともに記録する。
- 不明な点は「未確認」または TODO(verify)。既存コードと憲法に矛盾があれば報告する。
- raw bytes を破棄しない。実 capture は private、0600、data/captures/ に保存し Git 対象外。testdata は synthetic または明示的匿名化済みだけ。
- 将来の保存時は匿名化をデフォルト ON とし、local secret を使った HMAC 等を internal/privacy に隔離する。raw MAKA を無確認で正規化 JSON に埋め込まない。
- cmd は配線のみ。標準 flag / slog、最小依存。protobuf は google.golang.org/protobuf の実行時 descriptor 変換。MCP は Phase 4 で公式 Go SDK を第一候補とし、代替は理由記録と承認が必要。
- game version と liqi resource version を同一視しない。キャッシュは SHA / version 一致を必須とし、別版への fallback を禁止。
- capture / decode / extract / privacy / store / mcp は独立テスト可能にする。未知 message と orphan response で capture 全体を終了しない。接続単位で request correlation を管理する。
- 正規化は実測のみ。連荘を区別する hand_index、正確な decision 対応を使う。MAKA score の方向・差分の意味を確認するまで find_mistakes を完成扱いしない。
- gofmt、go vet、staticcheck、go test ./... を実施。通常テストは外部接続不要、live は build tag で分離。
- Goroutine は context cancellation と終了待機を必須とする。境界入力を検証し、context 付き error を返す。通常ログに raw / 個人情報を出さない。
- 依存理由・設計判断は docs/decisions.md。既存変更の reset / revert、raw データの commit は禁止。

Phase 完了報告は変更、確認、実測、未確認、TODO(verify)、主要ファイル、Acceptance Criteria を含め、次 Phase 未着手を明示する。
