# Protocol findings

CONFIRMED = capture または使用する liqi / 実際の静的 GET で確認できた範囲。
CANDIDATE = 既存 OSS・憲法記載の候補・定義検索のみで実際の利用は未観測。
UNKNOWN = 未確認。CONFIRMED の型が存在しても実通信で使われることは別途確認が必要。

<a id="static-chain"></a>
## 2026-09-05: 公開静的 schema の取得

確認日時: 2026-09-05T15:54:24+09:00（実装した fetch-proto の成功ログ）。それ以前の同日調査でも個別の公開 HTTP GET を実施。

最終ビルドでも 2026-09-05T16:03:54+09:00 に `fetch-proto --cache-dir .cache/mjcap/liqi --sha256 f2955c3d10cf2d42bee9309f672c062540941ea0cffe1bd62e3f436c7afc404c` が成功。同じ両 version・SHA・descriptor 数を確認した（cached=false）。

| 項目 | 実測値 |
| --- | --- |
| game version（version.json の値） | `0.11.252.w` |
| liqi resource version（manifest prefix） | `v0.11.243.w` |
| liqi SHA-256 | `f2955c3d10cf2d42bee9309f672c062540941ea0cffe1bd62e3f436c7afc404c` |
| liqi bytes | 286815 |
| descriptor file 数 | 1 |
| message descriptor 数（nested 含む） | 1318 |
| enum descriptor 数 | 1 |
| transport / direction | HTTPS、静的 GET response を受信 |
| message name | 該当なし（ゲームメッセージは未観測） |
| 操作 | ターミナルから公開静的リソース取得。ブラウザ・ゲーム操作なし |
| 確認方法 | version → manifest → liqi の HTTP 200、SHA 計算、全 registry 構築 |

確認できた配布 chain:

1. [version.json](https://game.mahjongsoul.com/version.json) の `version` は `0.11.252.w`、`code` は `v0.11.252.w/code.js`。
2. [その resource manifest](https://game.mahjongsoul.com/resversion0.11.252.w.json) は `res["res/proto/liqi.json"].prefix = "v0.11.243.w"` を持つ。
3. [manifest prefix 配下の liqi](https://game.mahjongsoul.com/v0.11.243.w/res/proto/liqi.json) を取得できた。

`resversion` と `res/proto/liqi.json` の文字列は version.json が指す公開 code.js 内にも存在した。code.js は実行していない。
manifest URL の構成と prefix を付ける resource URL は実際の GET 成功で確認した。将来同じ layout が維持される保証はなく、解決に失敗したら未知 layout として停止する。
調査中、game version を付けた liqi URL `v0.11.252.w/res/proto/liqi.json` の GET は 403 だった。これは解決経路として採用していない。

同日取得した調査用静的ファイルの SHA:

| 静的ファイル | SHA-256 |
| --- | --- |
| version.json | `112108838b042eca0e7e455bb8cf7d76f000d12de81c7a8a328d8485ff09b6ce` |
| resversion0.11.252.w.json | `91accb83474e4a530ff9c5e7b9471e7156cdc30410a11dbc223d9f637babcd2f` |
| v0.11.252.w/code.js | `63ae7207dcef9cef6b7c0e18855ad81499e1723b9da590634885ee90b632189b` |

原本 liqi は Git 対象外の `.cache/mjcap/liqi/<sha>.json` に保存した。固定版 liqi は repository に同梱していない。

### active browser との一致は UNKNOWN

同日の [ゲームトップページ](https://game.mahjongsoul.com/) は Unity WebGL の起動 HTML を返し、`productVersion: "4.0.12"` と `Build/jp-WebGL-release-4.0.12(13).loader.js` を示した。
この表記と上記 version.json の関係、実際の Chrome がどの schema を使うかは未確認。
`4.0.12` と `0.11.252.w` を同じ version として扱わない。公開配布 chain の存在は CONFIRMED だが、現在のゲーム通信への適用可能性は UNKNOWN。
Phase 1 に進む承認後、手動で開いたブラウザの実通信・実際にロードした静的 URL と照合する必要がある。

<a id="schema-types"></a>
## 上記 liqi 内の type（定義の存在のみ CONFIRMED）

確認日時・両 version・SHA・transport は上表と同じ。
direction: 静的 schema の受信。該当操作: fetch-proto。確認方法: JSON の fields と protoregistry.Files.FindDescriptorByName の結果。

| message name | field | number | type |
| --- | --- | --- | --- |
| `.lq.Wrapper` | `name` | 1 | string |
| `.lq.Wrapper` | `data` | 2 | bytes |
| `.lq.ResGameRecord` | `error` | 1 | `.lq.Error` |
| `.lq.ResGameRecord` | `head` | 3 | `.lq.RecordGame` |
| `.lq.ResGameRecord` | `data` | 4 | bytes |
| `.lq.ResGameRecord` | `data_url` | 5 | string |

この表は descriptor 定義の確認のみ。frame envelope、response type との相関、data/data_url の使用条件・ゲーム通信の方向・MAKAとの関係は未確認。
Phase 0 のコードは上記 field number をハードコードせず、全定義を実行時に変換する。

## Phase 1 以降で必要な実測

以下はすべて未確認。憲法にある名前は CANDIDATE で、現在の通信として確認していない。

- WebSocket frame の 0x01/0x02/0x03、message number の endian、Wrapper の配置。
- connection identifier、REQUEST/RESPONSE 相関、orphan response、close 時の挙動。
- 牌譜取得 message（候補 `.lq.Lobby.fetchGameRecord`）、実 response、HTTP body の関係。
- GameDetailRecords 相当の実 bytes、records/actions の使用、内部 action、圧縮/難読化の有無。
- MAKA 分析 request message name、response message name、protobuf type。
- MAKA 結果 transport（WebSocket / HTTP / 両方）。
- 半荘評価 field、局評価 field、候補 field、候補 score field。
- score の型、単位、値域、高低どちらが良いか、単純な差に意味があるか。
- 評価 rank の表現、MAKA の対象 seat。
- MAKA の局識別、巡目 / action / decision 識別。
- 牌譜と MAKA の結合 identifier / algorithm。連荘を含む hand と actor の正確な対応。
- 結果の再取得動作と有効期限。
- 牌譜 / MAKA の個人識別 field と匿名化対象。

MAKA message name の確定報告はまだできない。Phase 1 の実装状況は以下に記録する。

## 2026-09-05: Phase 1 実装・実測待ち

ユーザーの「phase進めて」により Phase 1 が承認された。
`capture` / `inspect`、CDP 観測、private JSONL、確認済み evidence profile に基づく最小 Wrapper/name decode と接続別相関を実装した。ゲーム envelope の推測定数や MAKA API は追加していない。

2026-09-05 16:47 JST までの接続確認では `127.0.0.1:9222` の HTTP discovery に接続できず、同ポートの LISTEN process もなかった。sandbox 外でも確認した。
これはローカル CDP endpoint の状態確認であり、雀魂・MAKA のプロトコル実測ではない。
game version / liqi resource version / message / direction / transport / UI 操作との相関は実 capture がないため **未確認**。
確認済みの実ゲーム用 envelope profile はまだ作成していない。合成 profile を実ゲームへ転用していない。

Phase 0 で取得した SHA `f2955c3d10cf2d42bee9309f672c062540941ea0cffe1bd62e3f436c7afc404c` の liqi について、`analy / review / evaluat / maka` を definition / method 名から検索した結果、`.lq.RecordAnalysisedData` が見つかった。
その定義は `round_infos`（repeated `RecordRoundInfo`、field 1）を持つ。`ai` 部分文字列は 94 definitions に一致したが、単語の偶然の部分一致を含み得る。
定義の存在以外、MAKA との対応・実通信での使用・score 意味は **CANDIDATE / 未確認**。分析関連名を本番の抽出ロジックとして使用していない。

[Phase 1 実測手順と受け入れ条件](phase1-capture.md) に従い、専用 Chrome でユーザー自身が牌譜・MAKA を開いた通信を確認する必要がある。
Phase 1 の受け入れ条件は未達。Phase 2 は未着手。

<a id="phase1-live-capture"></a>
## 2026-09-05: Phase 1 実捕捉（牌譜取得 + MAKA/Seer）

確認日時: 2026-09-05 17:53:38–17:57:49 JST。
capture: `data/captures/capture-20260905T085338.540811000Z.jsonl`（private、3,170 イベント、WebSocket 84 frame）。
環境: 専用 debug profile の Chrome 152.0.7977.82、`game.mahjongsoul.com` の page target 1 つに attach。ユーザーが手動で牌譜リスト表示 → 牌譜を開く → MAKA 表示 → 巡目送りを実施。
確認方法: capture JSONL の `payload_hex` を Phase 0 の liqi（SHA `f2955c3d…`）定義で調査用スクリプトにより動的 decode し、ゲーム UI のスクリーンショット表示値と突合した。調査スクリプトは repository 外。本番コードへの組み込みはしていない。

### version（実測値と未確認の関係）

| 項目 | 値 | 種別 |
| --- | --- | --- |
| 実クライアントが送信した `ReqGameRecord.client_version_string` | `WebGL_2022-0.16.239` | CONFIRMED（実測） |
| decode に使用した liqi | resource `v0.11.243.w` / SHA `f2955c3d…`（Phase 0 静的取得） | CONFIRMED（取得元） |
| 上記 liqi と実クライアント schema の同一性 | 対象 message すべてで unknown field 0 の decode 成功。**機能的整合は確認、同一性は未確認**（ブラウザが実ロードした liqi URL は attach 前のため未観測） | 部分確認 |
| `0.11.252.w` / `4.0.12` / `WebGL_2022-0.16.239` の相互関係 | 未確認 | UNKNOWN |

### WebSocket envelope（CONFIRMED）

transport: WebSocket 2 接続（CDP requestId `12925.323` / `12925.391`）。接続確立は attach 前のため URL は未観測。

- 送信 frame: `byte0=0x02`、byte1-2 = message number（little endian、16bit で収まる範囲を観測）、以降 `.lq.Wrapper`（`name`=fully qualified method 名、`data`=request bytes）。全 42 送信 frame が一致。
- 受信 frame: `byte0=0x03`、byte1-2 = 対応する送信と同一 number、以降 Wrapper で **`name` は空**、`data`=response bytes。全 42 受信 frame が一致。型解決には同一接続・同一 number の request との相関が必要（設計どおり）。
- number は本 capture では 42–83 が 2 接続にまたがって全体で単調増加していた。接続ごとに独立採番かは未確認。応答は常に同一接続・同一 number の要求に対応した。
- `0x01`（NOTIFY）は本セッションでは **未観測**（牌譜閲覧のみのため）。notify 側の wrapper offset は未実測であり、evidence profile（18 項目すべて実測必須）は notify 実測まで作成しない。

### 観測 message と操作の対応（すべて CONFIRMED、時刻は JST）

| 時刻 | message（送信、Wrapper.name） | 操作 / 内容 |
| --- | --- | --- |
| 定期 | `.lq.Route.heartbeat` | 両接続で定期送信 |
| 17:54:11 | `.lq.Lobby.fetchGameLiveList` | ロビー表示中 |
| 17:54:13 | `.lq.Lobby.loginBeat` | — |
| 17:54:13–14 | `.lq.Lobby.fetchGameRecordListV2` → `.lq.Lobby.fetchNextGameRecordList` | 牌譜リストを開いた |
| 17:54:19 | `.lq.Lobby.fetchSeerInfo` | 牌譜画面表示。応答 `ResFetchSeerInfo{remain_count=9, date_limit=0, expire_time=604800}` |
| 17:54:20 | `.lq.Lobby.fetchGameRecord` | **牌譜を開いた**。`ReqGameRecord{game_uuid, client_version_string}` |
| 17:54:20 | `.lq.Lobby.readGameRecord` | 牌譜オープン直後（目的は未確認） |
| 17:54:29 | `.lq.Lobby.fetchSeerReport` | **MAKA 表示**。`ReqFetchSeerReport{uuid}`（game_uuid と同一） |
| 17:54:29 以降 | heartbeat のみ | **巡目送り・分岐移動では追加通信なし**（評価は一括取得済み） |

`.lq.Lobby.fetchGameRecord` の応答 `ResGameRecord` は `head`（`RecordGame`: uuid/start_time/end_time/config/accounts/result/standard_rule）と `data`（74,055 bytes、inline）を持ち、**`data_url` はこのケースでは未使用**だった。data_url が使われる条件は未確認。

### MAKA = Seer 系 API（message 名と型は CONFIRMED）

MAKA の分析結果は `.lq.Lobby.fetchSeerReport` の応答 `ResFetchSeerReport{error, report: SeerReport}` で WebSocket 経由一括取得される。対象牌譜（uuid 先頭 `260905-c0abc18…`）で実測した構造:

- `SeerReport{uuid, events: repeated SeerEvent, rounds: repeated SeerRound}` — uuid は game_uuid と一致。events 472 件、rounds 10 件（東1〜南4 + 連荘 2 局。UI の局構成と一致）。
- `SeerRound{chang, ju, ben, player_scores: repeated SeerScore{seat, rating}}` — 4 seat 分の rating（観測値 5–56）。
- `SeerEvent{record_index, seer_index, recommends: repeated SeerRecommend{seat, predictions: repeated SeerPrediction{action: int32, score: int32}}}` — 全 seat が対象（seat0=111 / seat1=116 / seat2=119 / seat3=133 件）。predictions は最大 3 件で score 降順。
- `SeerPrediction.score` は 1–99 の整数。**UI 突合**: 南2局 10 巡目表示時の自家（seat=2）打牌バッジ「35 / 16 / 12」と一致する predictions `{35,16,12}` は全 472 events 中ちょうど 1 件（`record_index=614, seer_index=639, seat=2`）だった。バッジ数値 = `SeerPrediction.score` は CONFIRMED。

### CANDIDATE / UNKNOWN（Phase 2 以降で実測）

- `SeerScore.rating` のエンコード: 観測値は {5,15,16,24,25,26,35,36,44,45,46,54,55,56}。UI「一局評価 D+」表示中の南2局 seat=2 は rating=16。「十の位=グレード、一の位=修飾（-/無印/+）」は仮説（CANDIDATE）。
- 全体評価（UI「A-」）の取得元: SeerReport に該当 field なし。未確認。
- `SeerPrediction.action` の牌/行動エンコード（観測範囲 1–245。score=99 単独の action=1 は別種の行動の可能性）。
- score の方向: 降順先頭が UI の強調表示と整合するが「高いほど良い」の確定は Phase 3 で最善候補 UI と照合。
- `record_index` / `seer_index` と牌譜 `data` 内 records の対応（join key 候補）。
- `expire_time=604800`（7 日）が分析結果の有効期限を意味するか。
- NOTIFY frame（0x01）、notify wrapper offset、WS 接続 URL、data_url の使用条件。
- `RecordAnalysisedData` と MAKA の関係（本 capture では未使用の可能性が高いが未確認）。

### 既知の capture 品質問題

- WS 接続確立が attach 前だったため `Network.webSocketCreated` を観測できず、接続 URL が記録に無い。次回は capture 起動後にページを再読み込みする。
- ゲームアセット読み込みで HTTP body 予算（合計 128 MiB）が枯渇し、`body_budget_limit` が多発した。牌譜・MAKA は WebSocket のため今回の結論には影響しない。

<a id="phase1-reload-capture"></a>
## 2026-09-05: Phase 1 追加捕捉（リロードによる接続確立・ログイン・再取得）

確認日時: 2026-09-05 18:10:01–18:12:10 JST。
capture: `data/captures/capture-20260905T091001.860705000Z.jsonl`（private）。
操作: capture 起動後にユーザーがゲームページを手動リロード → 自動再ログイン → ロビーで待機 → 同じ牌譜と MAKA を再度手動で開いた。
確認方法は [前回](#phase1-live-capture) と同じ（liqi SHA `f2955c3d…` による動的 decode）。

### WebSocket 接続先（CONFIRMED）

`Network.webSocketWillSendHandshakeRequest` / `webSocketHandshakeResponseReceived` の requestHeadersText で確認:

| 接続 | URL | 備考 |
| --- | --- | --- |
| メイン（Lobby RPC を送出） | `wss://jpgs.mahjongsoul.com/gateway` | Cloudflare 経由 |
| サブ | `wss://jpgsbk.mahjongsoul.com/gateway` | heartbeat 中心 |

接続直前に HTTP `https://jpgs.mahjongsoul.com/api/clientgate/routes` と `https://jpgsbk.mahjongsoul.com/api/clientgate/routes`（application/json）を取得しており、gateway 探索とみられる（内容の解析は未実施）。
本 capture では `Network.webSocketCreated` イベント自体は記録に現れず、URL はハンドシェイクイベントから確認した。

### message number の採番（CONFIRMED 観測範囲）

- リロード後の新規 2 接続で number は **1 から再開**し、**両接続で共有された単調増加カウンタ**として振られていた（両接続の番号列が重複なく交互に補完し合う）。
- リロード前の旧 2 接続は 179–182 まで進んでいた（第1回 capture の 42–83 の継続。capture 外の期間も heartbeat で進行したことと整合）。
- 相関はいずれにせよ「同一接続 + 同一 number」で一意に取れた（設計どおり）。

### 接続開始・ログインの message（CONFIRMED、送信名）

新規接続の最初の送信は両接続とも `.lq.Route.requestConnection`。以後メイン接続で
`.lq.Lobby.oauth2Auth` → `oauth2Check` → `oauth2Login` → `loginSuccess` → `prepareLogin` →
`fetchAnnouncement` / `fetchInfo` / `fetchRollingNotice` / `fetchDailyTask` / `fetchChallengeInfo` / `fetchChallengeSeason` /
`fetchReviveCoinInfo` / `fetchAchievementRate` / `fetchQuestionnaireList` / `fetchConnectionInfo` / **`.lq.Lobby.fetchSeerReportList`** などを観測。
その後、前回と同じ `fetchGameRecordListV2` → `fetchNextGameRecordList` → `fetchSeerInfo` → `fetchGameRecord` → `readGameRecord` → `fetchSeerReport` の列を再観測した。

### MAKA 再取得（CONFIRMED）

同一牌譜への `fetchSeerReport` 再要求は、前回と同一内容（uuid 一致、events 472 件、rounds 10 件）を返した。分析済み牌譜の再表示は再計算でなく保存済みレポートの再取得とみられる（サーバ側動作の断定はしない）。

### NOTIFY は依然未観測

リロード → ログイン → ロビー待機 → 牌譜 + MAKA 再表示のすべてで frame は `0x02`/`0x03` のみ（50 送信 / 50 受信）。**`0x01` は 2 capture 通算で未観測**。
このため evidence profile（notify byte・notify wrapper offset の実測が必須）は引き続き作成しない。NOTIFY は対局中・観戦中・フレンド系イベント等で発生する可能性があるが未確認。

### 観戦（live game 視聴）は別プロトコル（2026-09-05 18:16–18:17 JST 追加 capture）

capture: `data/captures/capture-20260905T091648.025019000Z.jsonl`（private）。ユーザーが手動で対局観戦を開いた状態で観測。

- 観戦データは lobby とは**別の WebSocket 接続**（CDP requestId `12925.803`。接続確立が capture 開始前のため URL は未観測）で流れ、**lobby の 0x01/0x02/0x03 envelope を使わない**。
- 観測した frame 形式（CONFIRMED、意味は未解析）:
  - 送信: ASCII テキスト `<= FetchSequence …`（定期送信、29 bytes）
  - 受信: ASCII テキスト `=> <数値> {JSON}`（26 bytes 前後）
  - 受信: 先頭 1 byte が frame ごとに +1 される連番のバイナリ frame（内部に protobuf らしき length-delimited 構造。解析未実施）
- 同時間帯の lobby 接続（`12925.751`/`12925.771`）は heartbeat のみで、**観戦中も lobby 側に NOTIFY（0x01）は発生しなかった**。
- 観戦プロトコルの decode は本プロジェクトの目的（牌譜 + MAKA）の範囲外であり、実装しない。Phase 2 以降で「WS frame はすべて lobby envelope」と仮定してはいけない根拠として記録する。

NOTIFY（0x01）は 3 capture 通算で未観測のまま。lobby の非同期イベント（メール・フレンド等）で発生する可能性はあるが未確認。evidence profile は引き続き作成しない。

### schema のロード経路（新情報・未確認事項）

リロード時の HTTP リクエストに `liqi` / `proto` / `resversion` / `version.json` を含む URL は **1 件も無かった**（cache 提供分を含む）。
実クライアントの schema は Unity ビルド内蔵などの可能性があるが、取得経路は未確認。Phase 0 の静的取得 chain が「ブラウザが実行時にロードする経路」だと仮定してはいけないことが裏付けられた（decode の機能的整合は前回どおり成立）。

<a id="phase2-record-decode"></a>
## 2026-09-05: Phase 2 牌譜 decode の実測と検証

確認日時: 2026-09-05 18:30–18:40 JST。対象は [Phase 1 実捕捉](#phase1-live-capture) の capture に含まれる牌譜応答（uuid 先頭 `260905-c0abc18…`、金の間・四人南）。
確認方法: 実装した `mjcap decode`（liqi SHA `f2955c3d…` + lobby envelope profile）による decode と、調査スクリプト・ゲーム UI 表示との照合。

### 牌譜コンテナ（CONFIRMED）

- `ResGameRecord` は `head`（`RecordGame`）と `data`（74,055 bytes、inline）を持ち、`data_url` は未使用。
- `data` は `.lq.Wrapper` で、`name=".lq.GameDetailRecords"`。
- `GameDetailRecords`: `version=210715`、`records` は **空**、`actions` に **1352 件**の `GameAction`。実装は actions layout のみを対象とし、records layout と data_url は未実測として明示的に拒否する。
- `GameAction.type` の観測分布: type=1 が 828 件（`result` に Wrapper 包みの `.lq.Record*`）、type=2 が 518 件（`user_input`）、type=3 が 4 件（`user_event`）、type=4 が 2 件。type 値自体の意味は未確認で、result の有無だけを使用した。
- type=1 の `result` 名: `RecordDiscardTile` 407 / `RecordDealTile` 382 / `RecordChiPengGang` 18 / `RecordNewRound` 10 / `RecordHule` 10 / `RecordAnGangAddGang` 1。

### 牌コード（CONFIRMED）

`[0-9][mpsz]` 形式の文字列。`0m/0p/0s` は赤5（`RecordNewRound.doras` に `0s` がドラ表示牌として出現、チー `["4p","0p","6p"]`・ポン `["5s","5s","0s"]` も観測）。字牌は `1z`–`7z` を観測。

### 再構築の検証（CONFIRMED）

配牌（`tiles0..3`）＋ツモ（`RecordDealTile`）−打牌（`RecordDiscardTile`）−副露消費（`RecordChiPengGang` の `froms==seat` 分）−カン消費（`RecordAnGangAddGang`、手牌内の同一コード枚数 4→暗槓 4 枚 / 1→加槓 1 枚として決定）で全 seat の手牌を再生した結果:

- **407/407 の打牌すべてが打牌時点の再構築手牌に存在**（issues 0）。
- **和了 10 局すべてで `RecordHule.hules[].hand` と一致**: ロン 8 局は `hand` == 再構築手牌（`hu_tile` は含まれない）、ツモ 2 局は `hand` + `hu_tile` == 再構築手牌。
- **点数の連続性 9/9**: 各局 `RecordHule.scores` が次局 `RecordNewRound.scores` と一致。
- **UI 目視一致**: 南2局10巡目・自家の 14 枚（`4m4m 1p2p3p7p8p 3s4s6s8s 1z1z` + ツモ `3p`）とツモ切り `3p`、および南2局開始時の 4 座席点数表示が画面と一致。
- 親は「配牌 14 枚の seat」として全 10 局で一意に決定できた。全局で親 seat == `ju` だったが、これは観測であり規則としては未確定（実装は 14 枚判定のみを使用）。
- `RecordChiPengGang.type` は順子形で 0、刻子形で 1 を観測（CANDIDATE。実装未使用）。

### 匿名化対象（Phase 2 時点の列挙）

`RecordGame` の `accounts` / `robots`（`AccountInfo`）に `account_id`、`nickname`、`avatar_id`、`character`、`title`、`level`、`level3`、`avatar_frame`、`verified`、`views` を確認。`mjcap decode` の出力はこれらを一切読まず、seat 番号・牌・点数・uuid のみを出力する。
また、リロード capture には `oauth2Auth` 系 request の認証情報が raw のまま含まれる。capture が private（0600、Git 除外）であることが前提であり、正規化出力・ログへ auth/account 情報を出さない方針を維持する。

### 未実測のまま残る事項

- `data_url` 配送の実挙動、`records` layout、`GameAction.type` 値の意味、`RecordBaBei`（三麻）、`muyu` / `yongchang` などの未読 field、`RecordLiuJu` / `RecordNoTile` の詳細構造（終局種別の記録のみ実装）。

<a id="phase3-maka-join"></a>
## 2026-09-05: Phase 3 MAKA（Seer）結合の実測と検証

確認日時: 2026-09-05 19:00–19:20 JST。対象は [Phase 1 実捕捉](#phase1-live-capture) の同一牌譜（金の間・四人南、events 472 / rounds 10 の SeerReport）。
確認方法: 実装した `mjcap decode` の結合結果と、調査スクリプトによる全数照合、Phase 1 スクリーンショットとの突合。

### 結合キー（CONFIRMED）

- `SeerEvent.record_index` は **`GameDetailRecords.actions` の type=1 だけを数えた部分列の添字**で、「その判断を開いたゲームイベント」を指す。アンカー検証: 南2局10巡目の event（record_index=614）は type=1 部分列の 614 番目 = seat2 の 3p ツモ（直後の 615 番目が検証済みの 3p ツモ切り）。
- 開いたイベントの内訳（472 events）: 自分のツモ `RecordDealTile` 345 / 副露後 `RecordChiPengGang` 18 / 開局（親の第一打）`RecordNewRound` 10 / **他家の打牌** `RecordDiscardTile` 99（鳴き・ロン機会。recommend の seat は判断者であり打牌者と不一致 99/99）。
- 判断への解決規則: 同一 seat の次の `RecordDiscardTile`（打牌判断）、`RecordAnGangAddGang`（カン選択 1 件）、または `RecordHule`（和了選択 2 件）が局内に現れる。**全 472 events / 479 recommends が矛盾なく解決**（= 打牌判断 370 + 鳴き機会 106 + 和了 2 + カン 1）。
- 1 つの打牌が 2 seat に同時に機会を開く event が 7 件あり、`recommends` が 2 要素になる（それぞれ独立に結合）。
- MAKA event が付かない打牌は 37 件で、**37/37 がリーチ後の強制ツモ切り**だった。
- `SeerEvent.seer_index` は別カウンタで、リーチ・カン発生時に record_index との差が +1〜+2 ずつ広がる観測。意味は **未確認**、結合には使用しない。
- `SeerRound{chang, ju, ben}` は再構築した局（KyokuIndex, Honba）と 10/10 一致し、`player_scores` の rating を局へ結合した。

### `SeerPrediction.action` のエンコード（CONFIRMED）

- `action = 110 + 10×suit + rank`（suit: 0=m, 1=p, 2=s, 3=z / rank 0 = 赤5）は **その牌を切る**。全 1,038 個の該当予測すべてで、対応する牌が判断時の再構築手牌に存在した（矛盾 0）。
- `action = 210 + 10×suit + rank` は **リーチ宣言してその牌を切る**。該当 7 予測すべてで牌が手牌に存在し、実際にリーチした 7 判断すべてで宣言打牌が 2xx の牌と一致した。
- 小さい値は行動: `1` = 見送り（鳴き機会のほぼ全てで最高 score）、`7` = 和了（3 件すべてで直後にその seat の `RecordHule`）、`6` = カン（1 件、実行された）、`5` = ポン（1 件、実行された）。`5`/`6` は観測 1 件ずつのため CANDIDATE、`2`/`3`/`4` は出現したが対応行動は **未確認**。値そのものは正規化出力に raw のまま保持する。

### score の方向と正規化（CONFIRMED / 一部未確認）

- score は 1–99 の整数で、predictions は降順に並ぶ。**高いほど強い推奨**であることを次で確認した:
  1. UI は最高 score の候補（南2局10巡目では 東=35）を金色バッジで強調表示（Phase 1 スクリーンショット）。
  2. 和了推奨 99・カン推奨 97 はいずれも実行された。
  3. 実プレイヤーの打牌は 61%（225/370）で最高 score 候補と一致。
- これに基づき `score_delta_vs_best = best_score − actual_score`（0 = 最善）へ正規化する。実際の選択が上位候補（最大 3 件）に含まれない場合（23/370）は delta を **出力しない**（0 やゲタ値で埋めない）。
- score の厳密な意味（確率配分か評価値か、点数期待とどう関係するか）は **未確認**。差分は「推奨度の差」以上の意味を主張しない。

### UI との受け入れ照合

南2局10巡目（検証済みスクリーンショット）: 正規化 JSON の candidates は `{1z:35, 4s:16, 8p:12}` で、画面のバッジ（東の上に 35、4s の上に 16、8p の上に 12）と数値・位置とも一致。実打牌（3p ツモ切り）は候補外でバッジも無く、JSON でも `actual_score` / `score_delta_vs_best` が欠落する（**非最善打の一致確認**）。局（南2局）・seat（自家）・巡目（10）の対応も UI 表示と一致した。

### 未確認のまま残る事項

- `seer_index` の意味。`SeerScore.rating` のグレード表示への変換規則（十の位=グレード仮説は CANDIDATE のまま raw 出力）。UI「全体評価」の取得元。action `2`/`3`/`4`。複数和了（ダブロン）時の解決順。`fetchSeerReportList` / `fetchSeerInfo` の各 field 意味。

<a id="board-state"></a>
## 2026-09-06: リーチ棒・点数変動の実測（盤面復元用）

確認日時: 2026-09-06 17:20 JST。対象は Phase 1 capture の牌譜（全 5 リーチ・全 10 和了）。

- `RecordDealTile.liqi`（`LiQiSuccess`、直前巡に宣言した seat の次ツモに付く）: `score` は **1000 点供託後の宣言者の持ち点**、`liqibang` は **その時点の累積供託本数**。観測した全 5 リーチで `score == 直前の持ち点 - 1000`、`liqibang` は 1,2,1,2,1,1,1 と単調に一致した。`failed` は全て false。
- `RecordHule.old_scores` は供託控除込みの和了直前の点数で、上記から追跡した盤面残高と **10/10 の和了すべてで一致**（この照合は extract の恒常検証として実装済み。矛盾は round issue になる）。
- `RecordNewRound.liqibang` は持ち越し供託（全局 0 を観測 — 供託は全て和了で回収されたため。非 0 の実測は未取得だが field 名と 0 値は確認）。
- `RecordNewRound.left_tile_count` / `RecordDealTile.left_tile_count` は残り牌数としてそのまま使用（南2局 10 巡目で 40、東2局終盤で 4 を観測、進行と整合）。
- `RecordChiPengGang.scores` / `liqibang` は本牌譜では一度も出現せず **未実測**（使用しない）。

これらに基づき、各打牌決断へ `board_before`（点数・ドラ表示・残り牌数・供託・リーチ宣言・河・副露）を付与した。河は鳴かれた牌も残す（鳴かれは melds の froms で判別）。
