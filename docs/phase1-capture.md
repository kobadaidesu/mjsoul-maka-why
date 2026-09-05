# Phase 1 実測手順

Phase 1 のコードと合成テストは実装済み。実 Chrome への attach、牌譜通信、MAKA 通信、実クライアントの schema は未確認のため、Phase 1 は完了していません。

## 初回: raw の観測

1. README の専用 debug profile で Chrome を起動する。通常 profile を流用しない。
2. ユーザー自身でゲームページを開く。デフォルトでは `game.mahjongsoul.com` の既存 page target が一つ必要。複数ある場合は不要な専用タブを手動で閉じるか `--target-id` を指定する。
3. `./bin/mjcap capture --log-names` を起動する。`capture ready` を待つ。
4. 牌譜を開く、MAKA 画面を開く、必要なら分析開始、再生、MAKA 表示、巡目送りを順番に手動で行い、操作時刻を控える。初期 schema の通信を確認する際の再読み込みも手動で行う。
5. Ctrl-C で終了する。停止までに受信したイベントを保存して CDP socket を切断する。Chrome・タブは閉じない。
6. `./bin/mjcap inspect data/captures/<capture>.jsonl` で offline 集計する。

初回 `--log-names` の `protocol_unverified` は意図した状態。旧 OSS の envelope 値や旧 schema を現在の通信として推測使用しないため、実測前の組み込み形式はない。
raw capture にある frame と HTTP body を private な環境で調査し、実ブラウザの game version / liqi URL・SHA / envelope / request-response pair を確認して protocol-findings.md に記録する。
静的配布元の Phase 0 schema を、実際のブラウザが使用していると仮定してはいけない。

## 確認後: name ログ

実測が済んだ形式だけについて、Git 対象外の `.cache/` に profile を作成する。
`schema_version`、`evidence_level=CONFIRMED`、`evidence_ref`（実 capture と seq、確認方法）、`game_version`、`liqi_resource_version`、`liqi_sha256` が必須。
さらに Wrapper の型と name/data field、type offset、notify/request/response の各値、Wrapper offset、number offset/bytes/byte order を全て明示する。未記入の zero default は認めない。
field number は profile の field 名から descriptor で解決する。profile の CONFIRMED は調査者による実測の宣言であり、ファイルを作っただけで証拠になるものではない。

実ゲーム用 profile はまだ作成していない。`testdata/capture/protocol.json` は独自の合成プロトコル専用であり、雀魂へ流用してはいけない。

```sh
./bin/mjcap capture --log-names \
  --liqi-meta .cache/mjcap/liqi/<binding-sha>.meta.json \
  --protocol .cache/<confirmed-envelope>.json

./bin/mjcap inspect --log-names \
  --liqi-meta .cache/mjcap/liqi/<binding-sha>.meta.json \
  --protocol .cache/<confirmed-envelope>.json \
  data/captures/<capture>.jsonl
```

capture / inspect は schema をネットから自動取得しない。元 bytes・metadata・profile の両 version と SHA が一致する場合だけ解析する。
profile と schema metadata は capture_context に保存する。再解析時、capture に記録済みの binding と指定する schema / envelope が違う場合は拒否する。
capture 開始前の REQUEST がない RESPONSE は `type_unresolved` / `missing observed request`、未知の型は `unknown_message` とし raw を維持する。
REQUEST state は CDP connection ID と number で区別し、close / 再作成 / session 境界で破棄。同一接続の未完了 number 再利用は曖昧として解決しない。

## HTTP と保存の範囲

- `responseReceived` の metadata と元 params（headers 含む）を保存する。
- `loadingFinished` を保存した後に、選択した response の body だけ `getResponseBody` する。
- 元 CDP params/result、WebSocket payload hex、HTTP body base64 を private JSONL に保持。CDP が返す HTTP body は Chrome が提示した decoded body であり、圧縮前後や文字コード変換前の HTTP wire bytes と同一だとは主張しない。
- 選択した HTTP response の metadata state は 4096 件まで。上限に達した場合も元イベントを保存し `metadata_limit` と記録する。
- body 単体 32 MiB、合計 128 MiB を標準とし、取得中の各 body に単体上限を予約する。圧縮 body が取得後に大きくなっていた場合は、既に得た bytes を捨てず `stored_over_limit` として保存する。この場合、予算を超える可能性がある。
- 30 秒間返らない body は `body_timeout`。遅れて来た reply も CDP ID と元 result を保持する。終了時には未完了 body を記録する。
- WebSocket payload は容量フィルタで破棄しない。保存先の空き容量は必要。書き込み・fsync 失敗は明示的に終了し、残ったファイルを上書きしない。
- JSONL が途中で切れていたら inspect は破損位置を報告し、黙って読み飛ばさない。単一 JSONL record の読み込み上限は 256 MiB。
- 普通のログと debug ログにも raw payload / URL query / headers / 任意の CDP error を直接出さない。個人情報の混入を防ぐため、payload 先頭 hex 等の調査は private capture の seq を参照して行う。

## 制限と未確認

- 既存の一つの page target に接続する。worker / 別タブの通信、接続前の通信は捕捉できると保証しない。実測で通信が別 target にあると判明した場合に観測範囲を検討する。
- CDP の body eviction、stream、キャッシュ、navigation による取得不能は状態として保存する。独自 HTTP 再取得による穴埋めは禁止。
- Chrome から配信されるすべてのイベントを必ず取得できるという保証はない。実ゲームとの照合が受け入れ条件。
- MAKA の message、transport、fields、score semantics、seat、join、再取得・期限は未確認。Phase 2 の domain decode は行っていない。

## Acceptance Criteria（未達）

- [x] 観測・JSONL・Wrapper/name 相関の合成テストと必須 lint
- [ ] 専用 Chrome への実 attach
- [ ] 現行ブラウザ schema / envelope の実測
- [ ] 最低一つの牌譜取得通信
- [ ] MAKA UI 操作と通信の対応
- [ ] game/resource version、transport、message、direction、操作、response、fields、未確認事項の実測記録
- [ ] 実測した MAKA message 名の報告

Phase 2 は未着手・未承認。
