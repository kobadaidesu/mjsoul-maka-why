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

状態: **実装完了・PR待ち**（実機で attach→capture→decode 連結を確認。実牌譜での保存確認は次回の取り込み時）

- 目的: 「Chrome 起動 → capture → decode --games-dir」の 3 手順を `mjcap ingest` 1 コマンドにする。
- 設計: capture と同じ attach → `ingest ready` 表示 → ユーザーが牌譜 + MAKA を手動で開く → Ctrl-C → その capture を即 decode して store へ。既存の capture/decode 実装を配線するだけで新しいプロトコル知識は持たない。
- `--liqi-meta` / `--protocol` は省略時に自動探索: `.cache/mjcap/liqi/*.meta.json` と `.cache/mjcap/protocol/*.json` がそれぞれ**ちょうど 1 個**ならそれを使い、複数あれば明示指定を要求（推測選択はしない）。
- 受け入れ: 実 Chrome で牌譜を開いて 1 コマンドで data/games/ に保存されること。既存テストが緑のまま。

### ② 自席（self_seat）の自動判定 — branch `feature/self-seat`

状態: **実装完了・PR待ち**（実 capture で self_seat=2 を検出、UI 検証済みの席と一致。ログイン通信の無い capture では従来どおり seat 必須）

- 目的: `find_mistakes` の seat 指定を省略可能にする。
- 設計: capture 内のログイン応答（`.lq.Lobby.oauth2Login` の応答 `.lq.ResLogin`。field は decode 時に descriptor から解決）から自分の account_id を**メモリ上でのみ**取得し、`RecordGame.head.accounts[].{account_id, seat}` と照合して **seat 番号だけ**を `Game.SelfSeat` に保存する。account_id・nickname は保存もログもしない（憲法 §25/§45）。
- capture にログイン通信が無い場合（attach がログイン後）は判定不可 → SelfSeat 省略、従来どおり seat 必須。
- `find_mistakes`: seat 省略時、stored SelfSeat があればそれを使用（憲法 §29 の仕様どおり）。無ければ従来の明示エラー。
- 注意: ResLogin の account_id field 名は実測（capture 2 のリロードログイン応答）で確認してから使うこと。

### ③ 盤面の完全復元 — branch `feature/board-state`

状態: **実装完了・PR待ち**（LiQiSuccess の意味を実測で確定し、実牌譜 10 局で old_scores 照合 10/10・供託/残り牌数の整合を確認。findings #board-state 参照）

- 目的: 各決断に「河・副露・各家のリーチ状態・残り牌数・供託」を付け、LLM 解説を具体化する。
- 設計: extract の再生ループは既に全イベントを舐めているので、状態を広げるだけ。
  - 河: DiscardTile を seat 別に蓄積（ChiPengGang で最後の 1 枚が取られる点に注意 — froms の他家分が「取られた牌」）。
  - 副露: ChiPengGang / AnGangAddGang から tiles+froms を保存。
  - リーチ: is_liqi/is_wliqi の成立（LiQiSuccess が DealTile.liqi に来る点は要実測確認）と供託・点数変動。
  - 残り牌数: RecordDealTile.left_tile_count（実測済み field）をそのまま使う。
- 検証: 実 capture で「河の合計 + 手牌 + 副露 + 王牌」が矛盾しないこと、リーチ棒と点数連続性の整合。
- schema: 追加 field のみ（破壊的変更なしなので schema_version は 1 のまま。§28）。

### ④ 鳴き行動値の実測 — branch `feature/call-actions`

状態: **実装完了・PR待ち**（2=チー下/3=チー中/4=チー上/5=ポン/6=カン を実行18/18・フィージビリティ86/86で確定。findings #call-actions 参照。SeerCandidate.kind として出力）

- 目的: SeerPrediction.action の 2/3/4/5 を確定し、鳴き機会の分析を可能にする。
- 仮説（未確定・要実測）: 1=見送り(確定済), 5=ポン(1例確認), 6=カン(1例確認), 7=和了(確定済) から類推して 2/3/4=チーの 3 変化（喰い位置）ではないか。
- 実測方法: capture 1 の 18 件の実際の副露（RecordChiPengGang: type0=順子形/type1=刻子形）と、直前の call_opportunity イベントの候補 action を突合。チーの喰い位置（取った牌が順子の下/中/上）と action 値の対応を全件で検証する。
- 全件矛盾なしなら CONFIRMED として findings に記録し、`tileFromSeerAction` 同様の変換で SeerCandidate に kind ラベルを付与。矛盾が残る値は raw のまま。

### ⑤ プロンプト整備 — branch `feature/prompting`

状態: **実装完了・PR待ち**

- MCP server `instructions`（接続時に全クライアントへ渡る解釈ガイド: 牌表記、score の読み方と未検証の注意、kind 一覧、推奨フロー）。
- MCP prompt `analyze_game`（引数 game_uuid / seat とも省略可の定型分析依頼。クライアントの prompt メニューから利用可能）。
- 記載内容は実測済み事実と「未検証」の明示のみで構成し、findings と矛盾させないこと。

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
