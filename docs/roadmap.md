# Roadmap / 作業引き継ぎ

このファイルは進行中の改善作業の引き継ぎ資料です（Claude Code / codex どちらが続きを担当しても読めるように維持する）。
プロジェクトの憲法は [AGENTS.md](../AGENTS.md)、実測の根拠は [protocol-findings.md](protocol-findings.md)、設計判断は [decisions.md](decisions.md)。**実測なき推測実装の禁止**は改善作業にもそのまま適用される。

## 現在の全体像（2026-09-06 時点、Phase 0–4 完了）

```text
capture (CDP観測, internal/capture)
  → decode (envelope/Wrapper/dynamicpb, internal/decode)
  → extract (局・決断・手牌再生 + MAKA結合, internal/extract)
  → store (data/games/{uuid}.json, internal/store)
  → mcp (list_games / get_round / find_mistakes, internal/mcp)
```

- envelope・牌譜形式・MAKA(Seer) の実測は protocol-findings.md の
  `#phase1-live-capture` `#phase1-reload-capture` `#phase2-record-decode` `#phase3-maka-join` を参照。
- 実測用の private データ: `data/captures/`（capture 2 本 + 観戦 1 本）、`data/games/`（1 半荘、MAKA 結合済み）。
- envelope profile: `.cache/mjcap/protocol/lobby-envelope-20260905.json`、liqi binding: `.cache/mjcap/liqi/*.meta.json`。

## 改善キュー（ユーザー承認済み、各機能 1 ブランチ）

### ① ingest ワンコマンド取り込み — branch `feature/ingest`

状態: **main へ取り込み済み**（`bb50e81` = PR #4 merge。2026-09-10 に `git merge-base --is-ancestor` で main の祖先であることを確認）

- 検証実績（実装時）: 実機での attach→capture→decode 連結を確認。
- 未検収: 実牌譜の保存（「受信表示 → Ctrl-C → 保存結果」の一連照合。下記「未検収項目と手動検収手順」で実施）。2026-09-10 の取り込み確認はコミットの祖先確認のみ。

- 目的: 「Chrome 起動 → capture → decode --games-dir」の 3 手順を `mjcap ingest` 1 コマンドにする。
- 設計: capture と同じ attach → `ingest ready` 表示 → ユーザーが牌譜 + MAKA を手動で開く → Ctrl-C → その capture を即 decode して store へ。既存の capture/decode 実装を配線するだけで新しいプロトコル知識は持たない。
- `--liqi-meta` / `--protocol` は省略時に自動探索: `.cache/mjcap/liqi/*.meta.json` と `.cache/mjcap/protocol/*.json` がそれぞれ**ちょうど 1 個**ならそれを使い、複数あれば明示指定を要求（推測選択はしない）。
- 受け入れ: 実 Chrome で牌譜を開いて 1 コマンドで data/games/ に保存されること。既存テストが緑のまま。

### ② 自席（self_seat）の自動判定 — branch `feature/self-seat`

状態: **main へ取り込み済み**（`4a3ecae`。2026-09-10 に main の祖先であることを git で確認）

- 検証実績（実装時）: 実 capture で self_seat=2 を検出し、UI 検証済みの席と一致。ログイン通信の無い capture では従来どおり seat 必須。2026-09-10 の取り込み確認はコミットの祖先確認のみで、再検証は行っていない。
- TODO(verify): `ResLogin.account_id` の照合実測は decisions.md（2026-09-06）に記載があるが、protocol-findings.md に日時・方法つきの記録が無い。findings へ根拠を補完するまで、この件に関する CONFIRMED の追加はしない。

- 目的: `find_mistakes` の seat 指定を省略可能にする。
- 設計: capture 内のログイン応答（`.lq.Lobby.oauth2Login` の応答 `.lq.ResLogin`。field は decode 時に descriptor から解決）から自分の account_id を**メモリ上でのみ**取得し、`RecordGame.head.accounts[].{account_id, seat}` と照合して **seat 番号だけ**を `Game.SelfSeat` に保存する。account_id・nickname は保存もログもしない（憲法 §25/§45）。
- capture にログイン通信が無い場合（attach がログイン後）は判定不可 → SelfSeat 省略、従来どおり seat 必須。
- `find_mistakes`: seat 省略時、stored SelfSeat があればそれを使用（憲法 §29 の仕様どおり）。無ければ従来の明示エラー。
- 注意: ResLogin の account_id field 名は実測（capture 2 のリロードログイン応答）で確認してから使うこと。

### ③ 盤面の完全復元 — branch `feature/board-state`

状態: **main へ取り込み済み**（`d4f8c12`。2026-09-10 に main の祖先であることを git で確認）

- 検証実績（実装時）: LiQiSuccess の意味を実測で確定し、実牌譜 10 局で old_scores 照合 10/10・供託/残り牌数の整合を確認（findings #board-state 参照）。2026-09-10 は取り込み確認のみ。

- 目的: 各決断に「河・副露・各家のリーチ状態・残り牌数・供託」を付け、LLM 解説を具体化する。
- 設計: extract の再生ループは既に全イベントを舐めているので、状態を広げるだけ。
  - 河: DiscardTile を seat 別に蓄積（ChiPengGang で最後の 1 枚が取られる点に注意 — froms の他家分が「取られた牌」）。
  - 副露: ChiPengGang / AnGangAddGang から tiles+froms を保存。
  - リーチ: is_liqi/is_wliqi の成立（LiQiSuccess が DealTile.liqi に来る点は要実測確認）と供託・点数変動。
  - 残り牌数: RecordDealTile.left_tile_count（実測済み field）をそのまま使う。
- 検証: 実 capture で「河の合計 + 手牌 + 副露 + 王牌」が矛盾しないこと、リーチ棒と点数連続性の整合。
- schema: 追加 field のみ（破壊的変更なしなので schema_version は 1 のまま。§28）。

### ④ 鳴き行動値の実測 — branch `feature/call-actions`

状態: **main へ取り込み済み**（`4c43f1c`。2026-09-10 に main の祖先であることを git で確認）

- 検証実績（実装時）: 2=チー下/3=チー中/4=チー上/5=ポン/6=カン を実行18/18・フィージビリティ86/86で確定（findings #call-actions 参照。SeerCandidate.kind として出力）。2026-09-10 は取り込み確認のみ。

- 目的: SeerPrediction.action の 2/3/4/5 を確定し、鳴き機会の分析を可能にする。
- 仮説（未確定・要実測）: 1=見送り(確定済), 5=ポン(1例確認), 6=カン(1例確認), 7=和了(確定済) から類推して 2/3/4=チーの 3 変化（喰い位置）ではないか。
- 実測方法: capture 1 の 18 件の実際の副露（RecordChiPengGang: type0=順子形/type1=刻子形）と、直前の call_opportunity イベントの候補 action を突合。チーの喰い位置（取った牌が順子の下/中/上）と action 値の対応を全件で検証する。
- 全件矛盾なしなら CONFIRMED として findings に記録し、`tileFromSeerAction` 同様の変換で SeerCandidate に kind ラベルを付与。矛盾が残る値は raw のまま。

### ⑤ プロンプト整備 — branch `feature/prompting`

状態: **main へ取り込み済み**（`df580e2` で追加、`89d26b6` で analyze_game prompt をユーザー判断により削除し server instructions のみ残す。2026-09-10 に両コミットが main の祖先であることを git で確認）

- MCP server `instructions` のみ（接続時に全クライアントへ渡る解釈ガイド: 牌表記、score の読み方と未検証の注意、delta 不明≠0、河は鳴かれ牌も保持、kind 一覧、推奨フロー）。
- prompt テンプレート（analyze_game）は一度実装したが、素の依頼で同等の分析が出るためユーザー判断で削除。再追加するなら価値を再確認してから。
- 記載内容は実測済み事実と「未検証」の明示のみで構成し、findings と矛盾させないこと。

### ⑥ ingest の親しみやすい進行表示 — branch `feature/ingest-friendly-ui`

状態: **main へ取り込み済み**（実 Chrome で接続〜失敗案内までの表示は確認済み）

- 未検収: 実牌譜での `[rx] 牌譜/MAKA 受信` 行の実表示（下記「未検収項目と手動検収手順」で①と同時に実施）。

- 目的: `maka`（= `mjcap ingest`）実行時に、工程（接続 / スキャン中 / 牌譜・MAKA 受信 / 解析 / 保存）が `[ok]` / `[..]` / `[rx]` / `[!!]` / `[err]` のステータスタグで分かる進行表示を出す。
- 設計: `internal/cli/progress.go` の `friendly`（slog.Handler）が既存ログを変換するだけ。capture/decode は logger 注入（`runCaptureWith` / `runDecodeWith`）以外変更なし。キャッチ検出は `--log-names` の message 名（fetchGameRecord / fetchSeerReport / oauth2Login の received）を利用し、payload・URL・個人情報は表示しない。`body_budget_limit` 等の body 状態警告は表示から抑制（private capture には従来どおり全記録）。
- 既定 ON。従来の構造化ログは `mjcap ingest --plain`。

### ⑦ 過去 capture のログイン再利用による自席判定 — branch `feature/login-reuse`

状態: **完了（main へマージ済み）**（実データで確認: login なし capture の decode が別 capture のログイン応答を再利用し self_seat=3 を判定・保存）

- 目的: スキャン中にログイン通信が無くても self_seat を判定する。②の制約（attach がログイン後だと判定不可）の解消。
- 設計: decode 時に現 capture にログイン応答が無ければ、`--login-from-captures DIR`（ingest は既定で `data/captures` を渡す、`--reuse-login=false` で無効化）の capture を新しい順に走査し、最初に見つかったログイン応答の account_id を**メモリ上のみ**で席テーブルと照合する。HMAC 等の識別子保存はしない — raw capture が既に private (0600, Git 対象外) に ID を保持しているため、新たな保存先を作らない方が露出面が増えない。
- 複数アカウント対策（共有ディレクトリ）: 席テーブルに一致した既知 ID がちょうど 1 件のときだけ採用。0 件・複数件は従来どおり不明。
- ログには再利用元 capture のファイル名のみ出す。account_id は保存・ログ出力とも一切しない（憲法 §25/§45 維持）。

### ⑧ ingest の HTTP body 追加取得を既定で無効化 — branch `feature/ingest-http-bodies`

状態: **実装・コード/自動検証の検収完了（実機検収待ち・未マージ）**（2026-09-10 設計担当が 47f4fa5 の差分・テスト生出力を検収）

- 実装: `--http-bodies` を capture（既定 true）と ingest（既定 false）に追加。無効時は getResponseBody を一切発行せず、http_metadata は `body_status: "disabled"` で保存（URLPattern/selected より優先）。WebSocket・metadata・loadingFinished/Failed・遅着 CDP reply は従来どおり raw 完全保持。設定は capture_context の `http_bodies`（false でも省略しない）に記録。`ingest --http-bodies` で従来動作へ再有効化。
- 自動検証（合成テスト、2026-09-10）: 無効時 body 要求 0・pending/responses 追加 0、raw params/result の bytes 単位一致、WS イベントの有効/無効間一致、http_bodies 設定の異なる合成 capture を実 offline decoder に通した非空応答の完全一致、同一イベント列で無効側の合成 JSONL 3615→3198 bytes・body 要求 1→0（この実行での値であり実データの削減率ではない）、実 flag parse と runIngest→capture 受け渡し境界、旧 capture_context の後方互換、disabled の警告抑制、既存 budget/oversize 系の維持。
- 実機未確認: 削減後の実 capture サイズは未実測。根拠の実測値（2026-09-10 集計、MB は 10^6 バイト。尺度に注意: 以下は JSONL 行サイズ基準で、payload 実バイトとは別尺度）: 71 秒の capture の JSONL 全体 32.2MB、うち http_response 行の合計 28.9MB・websocket 行の合計 0.5MB。websocket の payload 実バイト合計は 0.15MB。budget は予約込み判定のため「128MiB 取得済み」の意味ではない。body 追加取得分の削減を見込むが、絶対サイズは保証しない。
- 検収手順（次回の手動 ingest 時、下記の①⑥手順と同時に実施可）: `./scripts/maka` → 牌譜+MAKA を開く → Ctrl-C 後、(a) capture 冒頭の capture_context に `"http_bodies":false` があること、(b) http_metadata が `"body_status":"disabled"` で、getResponseBody 由来の body 保存（`body_base64` 付き `http_response`）が無いこと — 遅着 `cdp_reply` の `CDPResult` 保持は正常であり不合格条件にしない、(c) `[rx]` 受信・MAKA 結合・保存が従来どおりであること、(d) capture ファイルサイズを従来（30MB 級）と比較して記録すること。

## 未検収項目と手動検収手順（次回の実取り込み時に実施）

対象: ①の実牌譜保存、⑥の実機 `[rx]` 受信行、⑧の実機削減効果。ユーザーの手動操作が必要（ゲーム通信の自動操作は憲法により禁止）。

手順:

1. `./scripts/maka`（または `maka`）を実行し、進行表示が `[..] スキャン中` になることを確認する。
2. Chrome の雀魂で牌譜を開き、MAKA を表示する（複数件可）。開くたびにターミナルへ `[rx] 牌譜 N 件目を受信` / `[rx] MAKA 評価 N 件目を受信` が出ることを確認する（⑥）。
3. ターミナルで Ctrl-C。`[ok] 保存: data/games/<uuid>.json` の行が、開いた牌譜の件数ぶん出て、該当ファイルが実在することを確認する（①）。
4. 終了コードが 0 であることを確認する（`echo $?`）。

記録項目（実施後、①⑥の状態欄へ日時つきで追記する）:

- 実施日時 / mjcap の git HEAD
- game_version・liqi resource_version（capture 冒頭の `capture_context`、または fetch-proto ログ）
- 開いた牌譜の件数・`[rx]` 表示の有無と件数・保存された件数・終了コード

## 完了済み機能の落ち穂（優先度低・未着手）

- NOTIFY(0x01) の実測 → envelope profile の notify 補完（ロビー通知が来るキャプチャ待ち）。
- `SeerScore.rating` → グレード表示（D+ 等）の変換規則確定（MAKA 画面のスクショ数枚で可能）。
- UI「全体評価」の取得元特定。三麻（RecordBaBei）。`seer_index` の意味。
- 雀魂アップデートで liqi が変わった際の追従導線（unknown field 検出時の案内強化）。

## 作業規約（引き継ぎ時の注意）

- ブランチ名は上記のとおり。**こまめに commit**、完了時に main 向け PR。コミット・PR に AI attribution は付けない。
- 実測が必要な項目は必ず capture から確認し、protocol-findings.md へ日時・方法つきで記録してから実装する。
- private データ（data/、.cache/）はコミットしない。プレイヤー名・account_id を保存・ログ出力しない。
- 完了したらこのファイルの該当セクションの「状態」を更新すること。
