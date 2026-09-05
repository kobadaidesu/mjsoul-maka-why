# mjcap — 雀魂 MAKA 牌譜観測

ユーザー自身がブラウザで開いた牌譜と MAKA 表示の通信を読み取り専用で観測し、将来 MCP から説明可能にする Go プロジェクトです。

Phase 0〜4 を実装済みです（各 Phase はユーザー承認を経て進行、2026-09-05 実捕捉ベース）。
`capture`、オフラインの `inspect`、牌譜 + MAKA 再構築と保存の `decode`、保存済みゲームを LLM へ提供する `mcp` が利用できます。
保存データはプレイヤー名・account 情報を含まないため、現状の匿名化は「識別子を読まない」ことで担保しています（name_hash 等の pseudonymization は識別子を保存する必要が生じた時点で `internal/privacy` に実装します）。

実捕捉により WebSocket envelope、`.lq.Lobby.fetchGameRecord`、MAKA（Seer 系 API `.lq.Lobby.fetchSeerReport`）を確認しました。詳細は [実測記録](docs/protocol-findings.md#phase1-live-capture) を参照してください。
静的取得した liqi は実通信を unknown field なしで decode できましたが、**実クライアントがロードした schema との同一性は未確認です**（NOTIFY frame も未観測のため evidence profile は未作成）。

## ビルドと確認

Go **1.26.5** と Make を使用します。Go major/minor・patch を `go.mod` と CI で揃えています。

```sh
go mod download
make build
make test
make tools
make lint
```

`make tools` は固定版 staticcheck v0.8.1 を `.tools/` にインストールします。
`make lint` は gofmt の確認、go vet、staticcheck を実行します。
初回の依存・tool 取得にはネット接続が必要ですが、通常テスト自体は Chrome・ゲーム・インターネットへ接続しません。

## fetch-proto

```sh
./bin/mjcap fetch-proto
```

実測した日本版公開配布元に対して、以下の静的リソースのみ HTTP GET します。

1. `version.json` から game version を読む。
2. 対応する resource manifest の `res["res/proto/liqi.json"].prefix` を読む。
3. manifest の prefix を使用して liqi を取得し、SHA-256 と descriptor registry を構築する。

game version と resource version は別々に保持します。URL の prefix は version 情報・manifest から毎回解決し、固定 liqi は同梱しません。
ログは stderr に出力し、両 version、取得元 URL、SHA-256、file/message/enum descriptor 数、候補 type の存在を表示します。

```sh
./bin/mjcap fetch-proto --cache-dir .cache/mjcap/liqi
./bin/mjcap fetch-proto --sha256 <確認済みの64文字SHA-256>
./bin/mjcap fetch-proto --timeout 90s --debug
```

`--sha256` は取得データと cache の両方を制約します。異なる SHA はエラーです。
`.lq.Wrapper` がなければ終了コード 1、期待候補 `.lq.ResGameRecord` がなければ警告を出します。別の型への推測置換はしません。

任意の API URL を入力する機能や redirect 追従はありません。HTTP の上限は version 64 KiB、manifest 32 MiB、liqi 8 MiB。各 GET は最大 30 秒、全体の標準 timeout は 90 秒です。自動 retry は行いません。

## Cache と再現性

標準 cache は `os.UserCacheDir()/mjcap/liqi/` です。
Linux では通常 `~/.cache/mjcap/liqi/`、macOS では通常 `~/Library/Caches/mjcap/liqi/` になります。

- `<liqi-sha256>.json`: 取得した liqi の exact bytes。
- `<binding-sha256>.meta.json`: game version / resource version / source URL の組み合わせごとの metadata。

metadata は `game_version`、`resource_version`、`source_url`、`sha256`、`fetched_at`、version/manifest URL、manifest SHA を保存します。
同じ liqi SHA を複数の game version が使用しても、それぞれの対応情報を保持します。
ディレクトリは 0700、ファイルは 0600。書き込みは同一ディレクトリの一時ファイルから fsync / close / rename / directory fsync で行います。

現在の version と manifest が解決できた後、liqi の GET が一時的な通信失敗または HTTP 5xx になった場合のみ、同じ game/resource version と source URL の cache を検討します。
metadata と実 bytes の SHA を照合し、指定時は `--sha256` も一致が必要です。
404、redirect、サイズ超過、schema 変換失敗、SHA 不一致を cache で隠しません。
version/manifest を確認できない完全オフライン状態では `fetch-proto` は失敗します。古い版への自動 fallback はありません。

`internal/liqi.Build` は bytes だけから registry を構築できます。合成 fixture と cache 内の exact liqi が将来のオフライン再処理の入力になります。
protobuf.js の proto3 JSON、nested message/enum、全 scalar、bytes、repeated、map、oneof、型・RPC reference、`proto3_optional` を扱います。
未知 schema 属性、proto2/editions、未対応 option、未解決 type は明示的なエラーです。未知 protobuf payload field は dynamicpb の roundtrip テストで保持を確認しています。

## Chrome の準備（Phase 1 用）

専用 debug profile を使います。macOS の手動起動例:

```sh
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
  --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port=9222 \
  --user-data-dir="$HOME/.local/share/mjcap/chrome-profile"
```

通常利用中の profile は使わないでください。既存 localhost endpoint への attach が標準です。外部 CDP は `--allow-remote-cdp` が必須です。
牌譜を開く、MAKA 分析開始、巡目を進める操作はユーザー自身が行います。

独自ゲームメッセージ送信、request replay、ゲーム API の fetch、MAKA 分析要求、`data_url` の独自 GET、自動クリック、Navigate/Evaluate による操作は実装しません。
CDP への送信は `Network.enable` と `Network.getResponseBody` にコードで限定しています。

## Phase 1 の capture / inspect

専用 Chrome でゲームページを手動で開いた後、以下を実行します。

```sh
./bin/mjcap capture --log-names
```

`capture ready` が出た後に、牌譜・MAKA 画面・巡目を手動で操作します。Ctrl-C で終了します。
未実測のフレーム形式は組み込んでいないため、確認済み profile がない初回は `protocol_unverified` と表示して raw の保存を続けます。
初回の raw と使用中の schema を実測で照合し、形式を確認した後に name ログを有効化します。詳細は [Phase 1 実測手順](docs/phase1-capture.md) を参照してください。

```sh
./bin/mjcap capture --duration 5m --out data/captures/manual-session.jsonl
./bin/mjcap inspect data/captures/manual-session.jsonl
```

capture 既存ファイルは上書きしません。JSONL は schema_version=1、process 内で増加する seq、CDP の元 params と payload を保持し、各行を fsync 後にログ解析します。
HTTP は metadata → loadingFinished → getResponseBody の順に処理し、失敗・対象外・上限超過を明示します。
標準の HTTP body 予算は単体 32 MiB / 全体 128 MiB。XHR/Fetch、JSON、octet-stream、protobuf を選択し、画像・音声・動画・font は除外します。
`--body-url-regexp` で body を保持する観測 URL を追加できます。URL への独自 GET は行いません。
上限と制約、name ログの evidence profile については実測手順を参照してください。

## Phase 2 の decode

capture 済み JSONL から牌譜応答を再構築します。実測済み envelope profile と exact liqi の指定が必須です。

```sh
./bin/mjcap decode --liqi-meta <binding>.meta.json --protocol <envelope>.json \
  [--game-uuid UUID] [--out data/games/<name>.json] CAPTURE.jsonl
```

出力は局ごとの `hand_index` / `kyoku_index` / 局ラベル・親・開始/終了点数・ドラ表示・全打牌決断（打牌前手牌、ツモ、ツモ切り、立直）を持つ JSON で、seat 番号と牌・点数・game uuid のみを含み、プレイヤー名や account 情報は読み取りません。
同じ capture に対応する `fetchSeerReport` 応答があれば MAKA も結合し、各打牌決断に候補（牌・リーチ・score）と `score_delta_vs_best`（0=最善、実選択が候補外なら省略）、局に seat 別 rating、鳴き機会・カン・和了判断を side evaluations として付与します（[記録](docs/protocol-findings.md#phase3-maka-join)）。
実測済みの inline `actions` layout だけを decode し、`data_url` 配送・`records` layout は未実測として明示的に失敗します。再構築で説明できない状態は `issues` として局に記録されます。
2026-09-05 の実牌譜では 10 局・407 決断を issues 0 で再構築し、全和了手牌・局間の点数連続性・UI 目視と一致しました。詳細は [実測記録](docs/protocol-findings.md#phase2-record-decode) を参照してください。

## Phase 4 の store と MCP server

`decode --games-dir data/games` が各ゲームを `{uuid}.json`（`schema_version: 1`、0600、atomic 書き込み）で保存します。

```sh
./bin/mjcap mcp --games-dir data/games
```

stdio の read-only MCP server が起動し、以下の 3 tool を提供します。

- `list_games` — 保存済みゲームの一覧（uuid、captured_at、局数、raw mode、最終点数、MAKA 結合有無）
- `get_round` — `hand_index` 指定で 1 局の全決断・手牌・MAKA 候補・rating を返す
- `find_mistakes` — `score_delta_vs_best >= threshold`（既定 10）の打牌を delta 降順で返す。**保存データはどの seat がユーザーかを持たないため `seat` は必須**（省略時は明示エラー）

server はゲーム・Chrome・ネットワークへ一切接続しません。実選択が MAKA 候補外だった決断は delta 不明として件数のみ報告します。

## ディレクトリと private data

`cmd/mjcap` は配線、`internal/cli` は CLI とログ、`internal/liqi` は静的取得・cache・parser・descriptor の責務です。
`internal/capture` は CDP と private JSONL、`internal/decode` は確認済み形式による Wrapper / name / request 相関のみを扱います。
将来層の `internal/{extract,model,privacy,store,mcp}` は空の足場のみです。

`data/captures/`、`data/games/`、`.cache/`、`*.log`、`privacy.key` は Git 対象外です。
実 capture は private data とし、`testdata/` へ直接コピーしません。現在の fixtures はすべて synthetic です。
将来、プレイヤー名や account id 等の正規化保存は local secret を使った匿名化をデフォルト ON とします。

## 未確認事項

- [x] 公開 static version → manifest → liqi の取得経路と異なる両 version — [記録](docs/protocol-findings.md#static-chain)
- [x] 取得 liqi 内の `.lq.Wrapper` / `.lq.ResGameRecord` — [記録](docs/protocol-findings.md#schema-types)
- [ ] 上記 schema と実際の Chrome クライアントの一致（unknown field なしの decode 成功で機能的整合は確認、同一性は未確認 — [記録](docs/protocol-findings.md#phase1-live-capture)）
- [x] WebSocket envelope、message number、request/response 相関、接続先 URL（`wss://jpgs.mahjongsoul.com/gateway` ほか）の現在の実通信（NOTIFY 0x01 のみ未観測） — [記録](docs/protocol-findings.md#phase1-reload-capture)
- [x] 牌譜取得 message（`.lq.Lobby.fetchGameRecord`）、data の inline 利用（data_url の使用条件は未確認） — [記録](docs/protocol-findings.md#phase1-live-capture)
- [x] MAKA request / response message name（`.lq.Lobby.fetchSeerReport` ほか）、transport（WebSocket 一括）、protobuf type（`SeerReport` 系） — [記録](docs/protocol-findings.md#phase1-live-capture)
- [ ] 半荘評価（UI「全体評価」）の取得元、局評価 rating のグレード表示規則（rating raw 値の結合は完了 — [記録](docs/protocol-findings.md#phase3-maka-join)）
- [x] score の方向（高いほど推奨）と `score_delta_vs_best` 正規化。score の厳密な意味・単位は未確認 — [記録](docs/protocol-findings.md#phase3-maka-join)
- [x] MAKA と牌譜の join key（`record_index` = type=1 部分列の添字。全 472 events 解決、未結合打牌はリーチ後強制のみ）と牌/リーチ action エンコード — [記録](docs/protocol-findings.md#phase3-maka-join)
- [ ] 結果の再取得動作、有効期限の実挙動（`expire_time=604800` を観測、意味は未確認）
- [x] 実牌譜の schema（`GameDetailRecords.actions`、version 210715）と `RecordGame.accounts` の匿名化対象列挙 — [記録](docs/protocol-findings.md#phase2-record-decode)

実測のない項目の実装・互換性仮定は行いません。[設計判断](docs/decisions.md) と [作業規約](AGENTS.md) も参照してください。

## 任意の live test

```sh
go test -tags live ./internal/liqi -run TestLiveStaticSchema -v
```

これは公開静的ファイルのみ取得します。標準の `make test` / CI では実行しません。
