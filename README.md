# mjcap — 雀魂 MAKA 牌譜観測

ユーザー自身がブラウザで開いた牌譜と MAKA（公式 AI）表示の通信を**読み取り専用**で観測し、牌譜を再構築して MAKA の評価と結合し、MCP 経由で LLM に説明させる Go プロジェクトです。

Phase 0〜4 は完了しています（各 Phase はユーザー承認を経て進行。実測の根拠は [docs/protocol-findings.md](docs/protocol-findings.md)、設計判断は [docs/decisions.md](docs/decisions.md)、進行中の改善は [docs/roadmap.md](docs/roadmap.md)）。

- ゲームサーバへ独自の通信は一切送りません（観測のみ。詳細は [AGENTS.md](AGENTS.md)）
- プレイヤー名・account id は保存もログ出力もしません（保存されるのは seat 番号・牌・点数・game uuid のみ）
- プロトコルの推測実装はせず、実測で確認できた形式だけを扱います

## クイックスタート

```sh
# 1. ビルドと schema 取得（初回のみ）
make build
./bin/mjcap fetch-proto --cache-dir .cache/mjcap/liqi

# 2. 専用 Chrome を起動（普段のプロファイルは使わない）
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
  --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port=9222 \
  --user-data-dir="$HOME/.local/share/mjcap/chrome-profile"

# 3. その Chrome で雀魂にログインし、取り込みを開始
./bin/mjcap ingest
#    → 牌譜を開いて MAKA を表示（複数件続けて可）→ Ctrl-C で自動 decode + 保存
#    ※ 2 と 3 は scripts/ingest-session.command 1 つで代替可（Dock やホットキーに置ける）

# 4. MCP サーバとして LLM クライアントに登録
claude mcp add --scope user mjcap -- "$PWD/bin/mjcap" mcp --games-dir "$PWD/data/games"
```

あとは LLM に「この半荘のミス打牌を MAKA の数値付きで解説して」と聞くだけです。

## コマンド

### fetch-proto — 公開 schema の取得

実測した日本版公開配布元に対して、静的リソースのみ HTTP GET します:
`version.json` → resource manifest → `res/proto/liqi.json`。game version と liqi resource version は別々に保持し、固定 liqi は同梱しません。

```sh
./bin/mjcap fetch-proto --cache-dir .cache/mjcap/liqi
./bin/mjcap fetch-proto --sha256 <確認済みの64文字SHA-256>
```

`--sha256` は取得データと cache の両方を制約します。redirect 追従・任意 URL 入力・自動 retry はなく、サイズ上限（version 64 KiB / manifest 32 MiB / liqi 8 MiB）と timeout を持ちます。
cache（既定 `os.UserCacheDir()/mjcap/liqi/`、`--cache-dir` で変更可）は exact bytes と version binding metadata を 0700/0600 + atomic 書き込みで保持し、SHA 不一致・404 等を cache で隠しません。

### ingest — ワンコマンド取り込み

capture → decode → 保存を 1 コマンドにしたものです（上のクイックスタート参照）。
`--liqi-meta` / `--protocol` は `.cache/mjcap/liqi/` と `.cache/mjcap/protocol/` にファイルがちょうど 1 つずつあれば自動選択し、複数あるときは明示指定を要求します（推測で選ばない）。

### capture / inspect — 観測と offline 解析

取り込みを段階ごとに実行したい場合や、raw の調査に使います。

```sh
./bin/mjcap capture --log-names --liqi-meta <binding>.meta.json --protocol <envelope>.json
./bin/mjcap inspect  --log-names --liqi-meta <binding>.meta.json --protocol <envelope>.json CAPTURE.jsonl
```

- CDP への送信は `Network.enable` / `Network.getResponseBody` に限定。Navigate / Click / Evaluate 等の自動操作は実装していません
- capture は private JSONL（schema_version=1、seq 単調増加、各行 fsync、既存ファイル上書きなし）へ raw を保存します
- HTTP body 予算は単体 32 MiB / 全体 128 MiB。`--body-url-regexp` で対象 URL を追加できます（独自 GET はしません）
- name ログには実測済み envelope profile が必須です。profile が無い間は `protocol_unverified` として raw のみ保存します（[Phase 1 実測手順](docs/phase1-capture.md)）
- 接続確立を capture に含めたい場合は、capture 起動後にゲームページを再読み込みしてください

### decode — 牌譜 + MAKA の再構築

```sh
./bin/mjcap decode --liqi-meta <binding>.meta.json --protocol <envelope>.json \
  [--game-uuid UUID] [--games-dir data/games] [--out FILE] CAPTURE.jsonl
```

出力（`--games-dir` 指定時は `data/games/{uuid}.json` に schema_version 付きで atomic 保存）:

- 局ごとの `hand_index` / `kyoku_index` / 局ラベル・親・開始/終了点数・ドラ表示
- 全打牌決断: 打牌前手牌、ツモ、ツモ切り、立直、**`board_before`**（点数〔リーチ供託控除込み〕・河・副露・各家リーチ状態・残り牌数・供託本数）
- MAKA 結合（同 capture に `fetchSeerReport` 応答がある場合）: 候補（牌・`kind`・score）と `score_delta_vs_best`（0=最善、実選択が候補外なら省略）、局別 rating、鳴き機会・カン・和了判断の side evaluations
- capture にログイン通信があれば **`self_seat`**（ユーザーの席番号のみ）を自動判定して保存
- 実測済みの inline `actions` layout のみ decode し、`data_url` 配送・`records` layout は未実測として明示的に失敗。説明できない状態は局の `issues` に記録
- 検証実績: 2026-09-05 の実牌譜で 10 局・407 決断を issues 0 で再構築（全打牌の手牌整合・全和了手牌一致・点数連続性・供託照合・UI 目視一致）

### mcp — LLM への提供（read-only）

```sh
./bin/mjcap mcp --games-dir data/games                          # stdio（既定）
./bin/mjcap mcp --games-dir data/games --listen 127.0.0.1:8930  # HTTP モード
```

3 つの read-only tool を提供します。server はゲーム・Chrome・ネットワークへ一切接続しません。

- `list_games` — 保存済みゲームの一覧（uuid、対局日時 start_time/end_time、captured_at、局数、raw mode、最終点数、self_seat、MAKA 結合有無）。並び順は対局日時の新しい順（取り込み順ではない）
- `get_round` — 1 局の全決断（盤面・MAKA 候補・rating 込み）
- `find_mistakes` — `score_delta_vs_best >= threshold`（既定 10）の打牌を delta 降順で返す。`seat` は保存済み `self_seat` があれば省略可、無ければ明示エラー（seat 0 への fallback はしない）。実選択が候補外の決断は delta 不明として件数のみ報告

接続クライアントには解釈ガイド（牌表記、score の読み方と未検証の注意、kind 一覧、推奨フロー）が MCP instructions として渡ります。

HTTP モードは既定で loopback にのみバインドし（非 loopback は `--allow-nonlocal-listen` 必須）、初回起動時に生成される秘密トークン（`data/mcp-token`、0600）を URL 先頭セグメントに要求します（不一致は定数時間比較で 404）:

```text
http://127.0.0.1:8930/<token>/mcp
```

web 版 LLM から使う場合は、この loopback URL の前に自分でトンネル（cloudflared 等）を張ります。**公開はユーザーの明示的な操作でのみ起き、トークン URL を知る相手はあなたの牌譜データを読めます。**不要になったらトンネルを止め、`data/mcp-token` を削除すればトークンは再生成されます。

## アーキテクチャ

```text
capture (CDP観測)  → decode (envelope/Wrapper/dynamicpb)
                   → extract (局・決断・盤面再生 + MAKA結合)
                   → store (data/games/{uuid}.json)
                   → mcp (list_games / get_round / find_mistakes)
```

`cmd/mjcap` は配線のみ。`internal/liqi` が静的取得・cache・schema 変換、`internal/capture` が CDP と private JSONL、`internal/decode` が実測済み形式の decode、`internal/extract` が牌譜再構築と MAKA 結合、`internal/store` / `internal/mcp` が保存と提供を担います。`internal/{model,privacy}` は将来用の足場です。
capture JSONL を入力すれば、Chrome やゲームサーバに接続せず decode → extract → store を完全再現できます。

## Private data とプライバシー

- `data/captures/`（**認証情報を含む** raw capture）、`data/games/`、`.cache/`、`data/mcp-token` は Git 対象外で、0700/0600 で保存されます
- 保存・出力にプレイヤー名・account id は含まれません。self_seat 判定も照合はメモリ上のみで、席番号だけを残します
- 実 capture を `testdata/` へコピーしません。fixtures はすべて synthetic です（[testdata/README.md](testdata/README.md)）
- 将来、識別子の正規化保存が必要になった場合は local secret による匿名化をデフォルト ON で `internal/privacy` に実装します

## 開発

Go **1.26.5** と Make を使用します。

```sh
go mod download
make build && make test && make tools && make lint   # lint = gofmt / go vet / staticcheck
go test -tags live ./internal/liqi -run TestLiveStaticSchema -v   # 公開静的取得のみの任意 live test
```

通常テストは Chrome・ゲーム・インターネットへ接続しません。作業規約は [AGENTS.md](AGENTS.md)、改善キューと引き継ぎは [docs/roadmap.md](docs/roadmap.md) を参照してください。

## 未確認事項

実測済みの主要事項（envelope、牌譜形式、MAKA の結合キー・牌/リーチ/鳴き action・score 方向、リーチ供託の点数変動、匿名化対象の列挙）は [docs/protocol-findings.md](docs/protocol-findings.md) に日時・方法つきで記録済みです。未確認のまま残っているのは:

- [ ] 静的取得した liqi と実クライアントがロードする schema の同一性（unknown field なしの decode 成功で機能的整合は確認済み — [記録](docs/protocol-findings.md#phase1-live-capture)）
- [ ] NOTIFY frame（0x01）と notify wrapper offset（3 capture 通算で未観測のため envelope profile の notify は宣言付き null）
- [ ] 半荘評価（UI「全体評価」）の取得元、局評価 rating のグレード表示規則（raw 値の結合は完了 — [記録](docs/protocol-findings.md#phase3-maka-join)）
- [ ] MAKA 結果の再取得動作と有効期限の実挙動（`expire_time=604800` を観測、意味は未確認）
- [ ] `data_url` 配送・`records` layout・`GameAction.type` 値の意味・三麻（`RecordBaBei`）・`seer_index` の意味・score の厳密な単位

実測のない項目の実装・互換性仮定は行いません。
