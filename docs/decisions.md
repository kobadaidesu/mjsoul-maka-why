# 設計・依存判断

## 2026-09-05: Phase 0 の範囲

作業対象は `/Users/kobadai/Documents/janntama`。初回確認では空で、Git、go.mod、go.sum、README、Makefile、cmd/internal/docs/tests は存在しなかった。既存コードとの矛盾はない。
隣接する `ryuusi` にはユーザー変更があったため触れていない。janntama のみ Git と module `mjcap` を初期化した。remote、commit、push は作成していない。

Phase 0 のみ実装。将来 package は空ディレクトリ保持用の `.gitkeep` のみ。capture、frame decode、MAKA field、正規化モデル、匿名化処理、MCP を先行実装しない。
`internal/cli` を配線とログの置き場として追加し、`cmd/mjcap` は process lifecycle の配線に限定した。

## Go と protobuf

既存 Go version がなかったため、作業環境で確認した Go 1.26.5 を go.mod と CI に固定した。標準 `flag` / `slog` / `net/http` / `encoding/json` / `crypto/sha256` を使用。

`google.golang.org/protobuf v1.36.11` を runtime の唯一の直接外部依存として採用。標準ライブラリに protobuf reflection / dynamicpb / descriptor validation がないため。
[公式 release](https://github.com/protocolbuffers/protobuf-go/releases/tag/v1.36.11) と [公式 module 定義](https://raw.githubusercontent.com/protocolbuffers/protobuf-go/v1.36.11/go.mod) を確認した。
release は 2025-12-12、Go 最低版 1.23。公式 repository の release・保守履歴を確認し、版を固定した。
module graph 上に github.com/golang/protobuf v1.5.0 と github.com/google/go-cmp v0.7.0 が存在する。アプリが使う runtime package graph は google.golang.org/protobuf のみであり、Cobra/chromedp/MCP SDK 等は追加していない。

protobuf.js JSON → descriptorpb.FileDescriptorProto → protodesc.NewFiles → protoregistry.Files の直接変換を採用。
公開 liqi 全体で構築が成功したため、.proto text 生成・protocompile を導入する必要はない。
proto3 が暗黙の既定であることは [protobuf.js Type.fromJSON](https://raw.githubusercontent.com/protobufjs/protobuf.js/master/src/type.js) の既定処理を確認した。
現在の liqi の message 定義は fields/type/id と repeated rule、namespace の go_package、nested 定義、enum、services を使用していた。

`rule: optional` は protobuf.js で省略可能な通常 label のため、presence を追加する根拠としない。
明示的 presence は `options.proto3_optional` に基づいて synthetic oneof を構築する。[Field 実装](https://raw.githubusercontent.com/protobufjs/protobuf.js/master/src/field.js) を参照。
oneof field は descriptor が要求する連続宣言順にまとめるが field number は変更しない。enum は source の宣言順を保持し、zero-first 違反を勝手に修正しない。
protobuf.js の未対応 properties/options や proto2/その他 editions は明示的に拒否する。見つかってから理由を調べて対応を追加する。

## Lint と CI

staticcheck は [公式 2026.2.1 release](https://github.com/dominikh/go-tools/releases/tag/2026.2.1) と module proxy の対応を確認し、v0.8.1 を `.tools/` に固定した。runtime の go.mod には追加しない。
最低 Go 1.26。tool 側の推移依存として BurntSushi/toml、google/go-cmp、x/exp、x/exp/typeparams、x/mod、x/sync、x/tools 等を [公式 go.mod](https://github.com/dominikh/go-tools/blob/2026.2.1/go.mod) で確認した。
`make tools` は初回 setup、`make lint` は gofmt / go vet / staticcheck の実行に分離する。CI も同じ pinned Go/tool で build/test/lint を行う。

## 静的 resolver とブラウザ版の不一致

[実測](protocol-findings.md) に基づく日本版静的配布元の経路だけを実装する。任意 API URL、他地域、別 URL layout への自動探索は提供しない。
毎回 version と manifest を GET し、liqi resource version は manifest の prefix としてそのまま保持する。game version から liqi version を算出しない。
redirect 禁止、GET のみ、cookies/auth/body なし、サイズ・timeout 上限あり。クライアント JS は実行しない。

トップページが Unity build を示す一方で、別系統の version/manifest/liqi が配布されている。どちらを使用中とするか推測で選ばず、静的 schema 取得の事実と active-browser 一致未確認を区別してログ・README に明示する。
Phase 1 ではユーザーの実ブラウザがロードする schema / build を確認する必要がある。確認できなければ別版を互換扱いしない。

## Cache

標準保存先は OS 推奨の os.UserCacheDir を採用。`--cache-dir` で変更可能。
raw liqi は content SHA をファイル名とし、metadata は game/resource/source binding の SHA をファイル名にする。
憲法の `<sha>.meta.json` は例示であり、同一 liqi を使う複数 game version の取得根拠を上書きしないため binding metadata を分けた。主要 metadata field は指定どおり。
0700 directory / 0600 file、atomic rename と fsync、read 時の SHA 検証を行う。通常ログに schema bytes は出さない。

cache fallback は current version/manifest が解決済みで、liqi GET の transport/read failure または 5xx の場合のみ。完全オフライン、404、redirect、schema/size/SHA mismatch では fail closed。
古い cache から current version を推測しない。cache の metadata は取得当時の manifest SHA を保持し、無関係 asset の manifest 更新だけで同じ version/source binding を破棄しない。

## 再現可能なテスト

testdata は独自の synthetic protobuf.js schema と payload のみ。実 liqi を fixture として同梱しない。
通常テストの HTTP は in-memory RoundTripper に置換し、localhost を含め socket を必要としない。
protobuf feature / 型解決 / raw unknown field roundtrip / cache version・SHA・権限 / redirect / size / cancellation / CLI validation を確認する。
実公開静的ファイル取得は `//go:build live` で分離する。

Phase 0 最終確認: `make build`、`GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local make test`（内部で `go test ./...`）、`make lint`（gofmt / go vet / staticcheck 2026.2.1）に成功。
最終バイナリの fetch-proto は実測済み SHA を明示して成功。Git 除外と cache/data directory の 0700、cache file の 0600 を確認した。
GitHub CI と build tag 付き live test 自体は未実行（公開取得の受け入れ確認は CLI で実施）。Phase 1 は未着手。

## 2026-09-05: Phase 1 承認後の実装

ユーザーの「phase進めて」を Phase 1 の承認として作業規約を更新。Phase 2 の domain payload decode は追加していない。

CDP 用に [chromedp v0.15.1](https://github.com/chromedp/chromedp/releases/tag/v0.15.1)（2026-04-01 公開）を固定した。
[固定版 go.mod](https://github.com/chromedp/chromedp/blob/v0.15.1/go.mod) と release / source を確認し、対応する cdproto `v0.0.0-20260321001828-e3e3800016bc` を使用。
標準ライブラリには CDP / WebSocket client がない。chromedp project が管理する typed CDP と transport を利用するための追加であり、Chrome 公式 Go SDK であるとは主張しない。
runtime の追加推移依存は chromedp/sysutil、go-json-experiment/json、gobwas/ws・httphead・pool、x/sys。
upstream の test/example graph に ledongthuc/pdf と orisano/pixelmatch も存在する。新しい Go version への変更は不要だった。

[固定版 Conn source](https://github.com/chromedp/chromedp/blob/v0.15.1/conn.go) を確認し、低水準の DialContext/Read/Write/Close だけを使用する。
高水準の allocator / browser actions を使用しないため、Chrome 起動、tab 作成、Runtime/Page enable、Navigate、Evaluate、Click、Browser.close は呼ばない。
接続先は Chrome の read-only `/json/list` から選択した既存 page target。localhost を数値 loopback に正規化し、socket の scheme / host / port / page path を別途照合する。
書き込み箇所は一つで、CDP `Network.enable` / `Network.getResponseBody` 以外を拒否するテストを置いた。
HTTP body 取得タイミングと WebSocket payloadData の UTF-8/base64 の扱いは [CDP Network 仕様](https://chromedevtools.github.io/devtools-protocol/tot/Network/) を確認した。

Conn.Read/Write が context を直接扱わないため、context cancellation で専用 socket を閉じる watcher を設ける。
reader と watcher の両方を WaitGroup で待ち、終了時も読み取り済みイベントの channel を drain して raw を保存する。未知ゲーム message と orphan response は停止理由にしない。

### 未実測 envelope の扱い

憲法は Phase 1 での最小 Wrapper/name decode を要求する一方、未実測の envelope 値を production logic に固定することを禁止している。
この両方を守るため、raw capture を先に実測し、証拠を持つ format profile を適用する。
profile は両 version / liqi SHA / evidence reference を必須にし、全 offset・type 値を明示させる。組み込みの実ゲーム用既定値はない。
`--log-names` 単独でも観測は始まるが、profile がない間は `protocol_unverified` として raw のみ保存する。これは Phase 1 完了ではない。
同じ JSONL を後から確認済み profile と exact cached schema で `inspect` できる。profile と metadata は capture_context に複製し、指定版と保存済み binding が違えば拒否する。
実測がない profile の CONFIRMED 宣言を作らない。synthetic fixture の宣言はその合成プロトコルにのみ有効。

### 保存・プライバシー・検証

JSONL schema_version=1。raw CDP params/result と decoded transport bytes を保持する。HTTP metadata、loadingFinished、body result の順序を seq で再現可能にした。
body の対象と予算は汎用的な transport 属性・明示 URL regexp で決め、ゲーム message / data_url を capture 層で解釈しない。
raw bytes、URL query、headers、任意 CDP error は private JSONL へ記録し、通常/debug ログへは直接出さない。失敗時の payload 先頭 bytes は private capture の seq から調査する。個人情報がないと確認せず公開ログへ出すことはしない。
capture は 0600 / parent 0700、O_EXCL で新規作成、各行 fsync。raw を先に保存してから name ログを呼ぶ。

合成 CDP transcript、binary/text、invalid base64、HTTP 完了順・failure・budget・body overshoot、unknown event、connection ごとの number、orphan、close、曖昧な number 再利用、profile/version mismatch、ログ privacy、終了待機をテスト。
`make build`、`make test`、`make lint`、`go test -race ./...` が成功。通常テストは in-memory fake transport を使い、Chrome/ゲーム/Internet へ接続しない。
実ブラウザ検証は endpoint 不在のため未実施。詳細は protocol-findings.md と phase1-capture.md に記録した。

## 2026-09-05: notify の declared-unobserved（Phase 1 実測後）

[実捕捉](protocol-findings.md#phase1-live-capture) で request(0x02)/response(0x03)/LE16 number/Wrapper 配置は確認できたが、NOTIFY frame は 3 capture 通算で一度も発生しなかった。
profile の全項目実測必須ルールのままでは、確認済みの request/response evidence すら使用できない。かといって notify に未実測値を書くのは推測の混入になる。
そこで profile の `notify` / `notify_wrapper_offset` に限り **明示的な JSON null**（= 未観測の宣言）を許可した。省略は従来どおり拒否し、暗黙の zero default は生まれない。
null の場合、request/response に一致しない frame は従来と同じ `unknown_frame_type` で raw のまま残る。NOTIFY を実測できたら profile を更新する。
Profile がポインタ field を持つため、capture binding の比較は struct 比較から `EquivalentProfiles`（EvidenceRef を除く JSON 比較）に変更した。
実ゲーム用 profile `.cache/mjcap/protocol/lobby-envelope-20260905.json`（Git 対象外）を上記実測から作成し、`inspect --log-names` で両 capture の全 frame が resolve されることを確認した。

## 2026-09-05: Phase 2 牌譜 decode の設計判断

ユーザー承認（「お願いします」）により Phase 2 に着手。追加依存はなし。

- 層の分離: `internal/decode` は frame → Wrapper → 相関 → dynamicpb の decode だけを持ち、`internal/extract` が局・決断への変換と手牌再生を担う。CLI `mjcap decode` は配線のみ。
- field 番号はハードコードせず、実測済みの field **名**（`data` / `actions` / `tiles0` など）を exact liqi の descriptor から解決する。欠けていれば fully-qualified 名付きの明示エラー。
- 実測済みの inline `actions` layout のみ実装。`data_url` 配送・`records` layout・未知 container 名は explicit error（TODO(verify) 付き）で拒否し、推測 decode をしない。
- 親判定は「配牌 14 枚の seat」というデータ由来の決定のみを使う。`ju` == 親 seat は全 10 局で観測されたが規則としては採用しない。
- 暗槓/加槓の区別は `type` 値を解釈せず、手牌内の同一牌枚数（4 → 4 枚除去、1 → 1 枚除去、他は issue）で決める。実牌譜の唯一の槓と合成テストで確認。
- 再構築で説明できない打牌・未実測 action 名は局の `issues` に記録して処理を続行する（capture 全体を落とさない）。
- 出力は seat・牌・点数・game uuid のみ。`RecordGame.accounts`（nickname / account_id 等）は読み取らない。匿名化保存そのものは Phase 3 以降の `internal/privacy` の責務。
- 検証: 実牌譜 10 局・407 決断 issues 0、全和了手牌一致（ロン 8: hand==再構築、ツモ 2: hand+hu_tile==再構築）、点数連続性 9/9、UI 目視 1 決断。`make build` / `make test` / `make lint` / `go test -race ./...` 成功。

## 2026-09-05: Phase 3 MAKA 結合の設計判断

ユーザー承認（「次のPhaseどうぞ」）により Phase 3 に着手。追加依存はなし。

- 結合は [実測した規則](protocol-findings.md#phase3-maka-join) のみ: `record_index` → type=1 部分列 → 同一 seat の次の打牌/カン/和了。`seer_index` は意味未確認のため使用せず raw 出力のみ。
- 解決できない event・複数 recommends の想定外形・範囲外 index は issue として記録し、推測で埋めない。実牌譜では issue 0 を確認。
- `score_delta_vs_best` は方向確認（UI 強調・実行された 99/97 推奨・best 一致率 61%）後に best − actual で正規化。実選択が候補外の場合は delta を出力しない（0 埋め禁止）。
- 牌エンコードは実測式（110/210 + 10×suit + rank、rank0=赤5）を関数 1 箇所に隔離し、範囲外 action は Tile 空のまま raw 保持。
- rating はグレード変換規則が未確認のため raw uint のみ出力。UI「全体評価」は SeerReport に無く、取得元未確認のため出力しない。
- 鳴き機会・カン・和了判断は `maka_side_evaluations` として局に保持（kind: call_opportunity / kan_decision / win_decision）。行動値の意味は findings に記録した範囲のみ注記し、コードでは解釈しない。

## 2026-09-05: Phase 4 store と MCP server

ユーザー承認（「マージした 次のPhaseどうぞ」）により Phase 4 に着手。

依存追加: [github.com/modelcontextprotocol/go-sdk v1.7.0](https://github.com/modelcontextprotocol/go-sdk)（MCP 公式 Go SDK、憲法の第一候補）。標準ライブラリに MCP 実装はない。
推移依存として google/jsonschema-go、go-json-experiment/json、segmentio/encoding・asm、yosida95/uritemplate、golang-jwt/jwt（graph 上）、x/oauth2・x/sync・x/sys を go.mod で確認した。stdio serving と in-memory テスト transport のみ使用し、HTTP/auth 系機能は使わない。

- `internal/store`: `data/games/{uuid}.json` に `schema_version: 1` 付きで atomic 保存（temp → fsync → rename → dir sync、0600/0700）。別 schema_version は silent 再解釈せず明示エラー。uuid はファイル名安全性を検証。`captured_at` は capture event の時刻。
- `mjcap decode --games-dir` が保存経路。store は decode 結果のみを扱い、ネットワーク・Chrome に触れない。
- `internal/mcp`: 公式 SDK の typed tool（`AddTool`）で read-only の `list_games` / `get_round` / `find_mistakes` を提供。ゲームへのアクセス機能は公開しない。
- self_seat はどの seat がユーザーかを特定できる実測がまだ無いため保存せず、`find_mistakes` は seat 省略時に憲法どおり明示エラーを返す（seat 0 への fallback をしない）。account_id 照合による自動判定は個人識別 field を読むため採用しなかった。
- mode は head.config の raw 値（category / mode / mode_id）のみ保存し、部屋名・長さへの変換は未実測のため行わない。UI「全体評価」も取得元未実測のため出力しない。
- `find_mistakes` は `score_delta_vs_best >= threshold`（既定 10）を delta 降順で返す。実選択が候補外の決断は件数のみ報告し、順位付けしない。
- 検証: 合成 store/mcp テスト（in-memory transport で 3 tool、seat 必須エラー、schema version 拒否、atomic 上書き）に加え、実牌譜を `--games-dir` で保存し、stdio の実プロセスに対して initialize → tools/list → 3 tool 呼び出し → EOF 正常終了を確認した。stdin EOF はクライアント切断として exit 0 とする。

### HTTP モード（ユーザー要望による追加）

web 版 LLM クライアントは stdio に接続できないため、`--listen` 指定時のみ streamable HTTP でも同じ read-only 3 tool を提供する（既定は従来どおり stdio）。
外部公開のリスクは次で opt-in に限定する: (1) 既定で loopback 以外へのバインドを拒否（`--allow-nonlocal-listen` が必須）、(2) 全リクエストに URL 先頭セグメントの秘密トークン（初回起動時に `data/mcp-token` へ 0600 生成、Git 除外、定数時間比較、不一致は MCP 処理前に 404）。
インターネット公開はユーザーがトンネルを自分で張った場合のみ発生し、その旨と失効手順（token ファイル削除）を README に明記した。静的 Bearer ヘッダを設定できないクライアント（ChatGPT connectors 等）でも使えるよう、ヘッダでなく secret-path 方式を選んだ。
検証: httptest + SDK StreamableClientTransport の合成テスト（誤トークン 404、正トークンで list_games）と、実プロセスに対する curl での initialize 成功・誤トークン 404 を確認。

## 2026-09-06: 改善① ingest / 改善② self_seat（roadmap 参照）

ユーザー承認により docs/roadmap.md の改善キューに着手。

- `mjcap ingest`: 既存 capture → decode の配線のみ。evidence ファイルは cache に候補がちょうど 1 つのときだけ自動選択し、0/複数は明示エラー（推測選択しない）。サブコマンド追加の必要性はユーザー要望による。
- self_seat: capture 内の `.lq.Lobby.oauth2Login` 応答（`ResLogin.account_id`、実測）と `RecordGame.accounts[].{account_id, seat}` をメモリ上で照合し、**seat 番号だけ**を `Game.SelfSeat` に保存する。account_id・nickname は保存もログもしない。照合できない capture では従来どおり省略。
- `find_mistakes` は seat 省略時に stored self_seat を使い（憲法 §29 の仕様どおり）、無ければ従来の明示エラーを維持。明示 seat は常に優先。
- 検証: 合成テスト（照合ヒット/ミス/ゼロ ID 拒否、MCP の default/override/エラー維持）に加え、リロード capture の再 decode で self_seat=2 を検出し、UI で確認済みの席と一致した。

## 2026-09-10: 改善キューの取り込み状況と検収状況の分離

採用: roadmap に main への取り込み根拠（コミットが main の祖先であることの git 確認）と検証状況を分けて記録する。理由: 改善①〜⑤の実装は main に含まれているが「PR待ち」の記載が残っていたため。

却下: main への取り込みをもって全受け入れ基準の達成とする案。理由: ingest の実牌譜保存と進行表示の実機受信行に未確認事項が残る。過去のテスト成功を現在 HEAD の検証結果として扱わない。

プロトコル確認状態は変更しない。self_seat の照合実測（`ResLogin.account_id`）は decisions.md 2026-09-06 に記載があるが protocol-findings.md に日時・方法つきの記録が無いため、根拠補完を別途行う（roadmap ②の TODO(verify)）。
