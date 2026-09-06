# Project rules

このプロジェクトはユーザー提示の「雀魂 MAKA 牌譜キャプチャ → MCP サーバー Project Constitution」に従う。

**状態: Phase 0〜4 はユーザー承認を経てすべて完了済み**（承認済み score 方向の実測により find_mistakes も完成扱い）。以降の作業は docs/roadmap.md の改善キューに沿って進め、機能ごとにブランチを分けてこまめに commit し、完了時に roadmap の状態を更新する。コミット・PR に AI attribution は付けない。

## 絶対制約（全作業に適用）

- ゲーム通信は観測専用。独自 WebSocket 送信、request replay、ゲーム API 呼び出し、MAKA 分析要求、ブラウザ自動操作、data_url の独自取得は禁止。
- HTTP GET は protobuf 定義取得に必要な公開静的リソースに限定する。
- プロトコルの message / URL / field / number / enum / score 意味を推測で実装しない。CONFIRMED / CANDIDATE / UNKNOWN を区別し、実測は docs/protocol-findings.md に日時、各 version、方向、transport、操作、方法とともに記録してから実装する。
- 不明な点は「未確認」または TODO(verify)。既存コードと憲法に矛盾があれば報告する。
- raw bytes を破棄しない。実 capture は private、0600、data/captures/ に保存し Git 対象外（ログイン認証情報を含むため特に厳守）。testdata は synthetic または明示的匿名化済みだけ。
- プレイヤー名・account id を保存もログ出力もしない（self_seat 判定の照合はメモリ上のみ、席番号だけを残す）。識別子の正規化保存が必要になったら local secret を使った匿名化をデフォルト ON で internal/privacy に隔離する。raw MAKA を無確認で正規化 JSON に埋め込まない。

## 実装規約

- cmd は配線のみ。標準 flag / slog、最小依存。protobuf は google.golang.org/protobuf の実行時 descriptor 変換。MCP は公式 Go SDK（採用済み、docs/decisions.md）。依存追加は理由を docs/decisions.md に記録する。
- game version と liqi resource version を同一視しない。キャッシュは SHA / version 一致を必須とし、別版への fallback を禁止。
- capture / decode / extract / privacy / store / mcp は独立テスト可能にする。未知 message と orphan response で capture 全体を終了しない。接続単位で request correlation を管理する。
- 正規化は実測のみ。連荘を区別する hand_index、正確な decision 対応を使う。
- gofmt、go vet、staticcheck、go test ./...（+ go test -race）を実施。通常テストは外部接続不要、live は build tag で分離。
- Goroutine は context cancellation と終了待機を必須とする。境界入力を検証し、context 付き error を返す。通常ログに raw / 個人情報を出さない。
- 既存変更の reset / revert、raw データの commit は禁止。

作業報告は変更、確認、実測、未確認、TODO(verify)、主要ファイルを含める。ドキュメントの入口は README.md（使い方）、docs/protocol-findings.md（実測）、docs/decisions.md（設計判断）、docs/roadmap.md（改善キューと引き継ぎ）。
