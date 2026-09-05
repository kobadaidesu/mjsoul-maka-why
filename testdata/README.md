# Fixtures

All files here are synthetic. `liqi/features.json` exercises protobuf.js schema
conversion and `liqi/packet.json` is its synthetic message payload. They are not
Mahjong Soul captures, protocol evidence, or a bundled game schema.

Phase 1 adds `capture/cdp.jsonl`, an invented CDP event transcript, plus
`liqi/wire.json` and `capture/protocol.json`. The latter is CONFIRMED **only for
this synthetic test protocol**, with deliberately different frame values (17,
34, 51) and Wrapper field numbers (7, 9). It must never be used to claim that
Mahjong Soul's envelope is confirmed. No real-game profile is shipped.

Phase 2 adds `liqi/gamerecord.json`. Its message names and field numbers mirror
the subset of the measured public liqi schema that record decode reads
(docs/protocol-findings.md#phase2-record-decode); every value used in tests
(tiles, scores, uuids) is synthetic and no real capture bytes are embedded.
It is a test schema, not a bundled game schema: production decode always uses
the exact liqi fetched at runtime.

Do not copy real user captures into this directory. Future capture fixtures
must be synthetic or explicitly anonymized, with provenance documented here.
