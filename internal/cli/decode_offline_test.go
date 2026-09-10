package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"mjcap/internal/capture"
	"mjcap/internal/decode"
	"mjcap/internal/liqi"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// buildDyn mirrors the synthetic message builder used by the decode/extract
// tests (testdata/README.md): dynamic messages from the fixture schema with
// synthetic values only.
func buildDyn(t *testing.T, reg *liqi.Registry, name string, fields map[string]any) *dynamicpb.Message {
	t.Helper()
	md, err := reg.Message(name)
	if err != nil {
		t.Fatal(err)
	}
	m := dynamicpb.NewMessage(md)
	for fn, v := range fields {
		fd := md.Fields().ByName(protoreflect.Name(fn))
		if fd == nil {
			t.Fatalf("fixture lacks field %s.%s", name, fn)
		}
		switch v := v.(type) {
		case string:
			m.Set(fd, protoreflect.ValueOfString(v))
		case bool:
			m.Set(fd, protoreflect.ValueOfBool(v))
		case []byte:
			m.Set(fd, protoreflect.ValueOfBytes(v))
		case int:
			switch fd.Kind() {
			case protoreflect.Int32Kind:
				m.Set(fd, protoreflect.ValueOfInt32(int32(v)))
			case protoreflect.Int64Kind:
				m.Set(fd, protoreflect.ValueOfInt64(int64(v)))
			default:
				m.Set(fd, protoreflect.ValueOfUint32(uint32(v)))
			}
		case []string:
			list := m.Mutable(fd).List()
			for _, s := range v {
				list.Append(protoreflect.ValueOfString(s))
			}
		case []int32:
			list := m.Mutable(fd).List()
			for _, n := range v {
				list.Append(protoreflect.ValueOfInt32(n))
			}
		case []uint32:
			list := m.Mutable(fd).List()
			for _, n := range v {
				list.Append(protoreflect.ValueOfUint32(n))
			}
		case map[string]any:
			m.Set(fd, protoreflect.ValueOfMessage(buildDyn(t, reg, "."+string(fd.Message().FullName()), v)))
		case []map[string]any:
			list := m.Mutable(fd).List()
			for _, child := range v {
				list.Append(protoreflect.ValueOfMessage(buildDyn(t, reg, "."+string(fd.Message().FullName()), child)))
			}
		default:
			t.Fatalf("unsupported fixture value %T", v)
		}
	}
	return m
}

func mustMarshal(t *testing.T, m *dynamicpb.Message) []byte {
	t.Helper()
	raw, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// offlineEvidence writes a real evidence pair (liqi binding metadata +
// cached schema, CONFIRMED envelope profile) for the synthetic fixture so
// runDecodeWith exercises the production evidence loading path. The URLs
// follow the measured public layout but the versions and bytes are the
// synthetic fixture only.
func offlineEvidence(t *testing.T) (metaPath, profilePath string, r *liqi.Resource) {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/liqi/gamerecord.json")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := liqi.Build(raw)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	m := liqi.Metadata{
		GameVersion:     "synthetic-game",
		ResourceVersion: "synthetic-schema",
		SHA256:          hex.EncodeToString(sum[:]),
		ManifestSHA256:  hex.EncodeToString(sum[:]),
		FetchedAt:       time.Now().UTC(),
		SourceURL:       "https://game.mahjongsoul.com/synthetic-schema/res/proto/liqi.json",
		VersionURL:      "https://game.mahjongsoul.com/version.json",
		ManifestURL:     "https://game.mahjongsoul.com/resversionsynthetic-game.json",
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	binding := sha256.Sum256([]byte(m.GameVersion + "\n" + m.ResourceVersion + "\n" + m.SourceURL))
	metaPath = filepath.Join(dir, hex.EncodeToString(binding[:])+".meta.json")
	metaJSON, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, metaJSON, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, m.SHA256+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	p := decode.Profile{
		SchemaVersion: 1, EvidenceLevel: "CONFIRMED",
		EvidenceRef:     "SYNTHETIC fixture only; not evidence of a Mahjong Soul protocol",
		GameVersion:     m.GameVersion,
		ResourceVersion: m.ResourceVersion,
		SHA256:          m.SHA256,
		WrapperMessage:  ".lq.Wrapper", NameField: "name", DataField: "data",
		TypeOffset: 0, Request: 2, Response: 3,
		RPCWrapperOffset: 3, NumberOffset: 1, NumberBytes: 2, ByteOrder: "little",
	}
	profileJSON, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	profilePath = filepath.Join(dir, "profile.json")
	if err := os.WriteFile(profilePath, profileJSON, 0600); err != nil {
		t.Fatal(err)
	}
	return metaPath, profilePath, &liqi.Resource{Data: raw, Registry: reg, Metadata: m}
}

// rpcFrame builds one envelope frame for the synthetic profile: type byte,
// 2-byte little-endian request number, Wrapper bytes.
func rpcFrame(kind byte, num uint16, wrapper []byte) []byte {
	return append([]byte{kind, byte(num), byte(num >> 8)}, wrapper...)
}

type wsFrame struct {
	direction string
	payload   []byte
}

// captureBase is the fixed reference clock for synthetic captures: the
// event with seq N is stamped captureBase + N seconds, so tests can assert
// exactly which event's timestamp was preserved.
var captureBase = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func captureEventTime(seq uint64) time.Time {
	return captureBase.Add(time.Duration(seq) * time.Second)
}

func writeWSCapture(t *testing.T, path string, frames []wsFrame) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for i, fr := range frames {
		seq := uint64(i + 1)
		e := capture.Event{SchemaVersion: 1, Seq: seq, CapturedAt: captureEventTime(seq), Kind: "websocket",
			ConnectionID: "c", Direction: fr.direction, Opcode: 2, PayloadHex: hex.EncodeToString(fr.payload)}
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// offlineRecordResponse mirrors the measured shapes with the seer_test
// scenario: one round, five discard decisions (action indexes 1,3,5,9,11),
// of which the post-riichi discard at 11 stays unjoined.
func offlineRecordResponse(t *testing.T, r *liqi.Resource, uuid string) []byte {
	t.Helper()
	reg := r.Registry
	hands := [][]string{
		{"1m", "2m", "3m", "5z", "5z", "5z", "7p", "8p", "9p", "1s", "2s", "3s", "6z"},
		{"1m", "1m", "2p", "3p", "4p", "5p", "6p", "7p", "1s", "2s", "3s", "7z", "7z", "9s"},
		{"1p", "2p", "3p", "4p", "5p", "6p", "7p", "8p", "9p", "1m", "2m", "3m", "7z"},
		{"4m", "5m", "9m", "9m", "1p", "1p", "2s", "2s", "3z", "3z", "4z", "4z", "6z"},
	}
	newRound := map[string]any{"chang": 1, "ju": 1, "ben": 0, "scores": []int32{25000, 25000, 25000, 25000}, "doras": []string{"4p"},
		"tiles0": hands[0], "tiles1": hands[1], "tiles2": hands[2], "tiles3": hands[3]}
	steps := []struct {
		name   string
		fields map[string]any
	}{
		{".lq.RecordNewRound", newRound},
		{".lq.RecordDiscardTile", map[string]any{"seat": 1, "tile": "9s"}},
		{".lq.RecordDealTile", map[string]any{"seat": 2, "tile": "1z"}},
		{".lq.RecordDiscardTile", map[string]any{"seat": 2, "tile": "1z", "moqie": true}},
		{".lq.RecordDealTile", map[string]any{"seat": 3, "tile": "9m"}},
		{".lq.RecordDiscardTile", map[string]any{"seat": 3, "tile": "9m", "is_liqi": true}},
		{".lq.RecordDealTile", map[string]any{"seat": 0, "tile": "4z"}},
		{".lq.RecordAnGangAddGang", map[string]any{"seat": 0, "tiles": "6z"}},
		{".lq.RecordDealTile", map[string]any{"seat": 0, "tile": "7z"}},
		{".lq.RecordDiscardTile", map[string]any{"seat": 0, "tile": "7z", "moqie": true}},
		{".lq.RecordDealTile", map[string]any{"seat": 3, "tile": "1s"}},
		{".lq.RecordDiscardTile", map[string]any{"seat": 3, "tile": "1s", "moqie": true}},
		{".lq.RecordDealTile", map[string]any{"seat": 2, "tile": "7z"}},
		{".lq.RecordHule", map[string]any{"hules": []map[string]any{{"seat": 2, "zimo": true}}, "scores": []int32{1, 2, 3, 4}}},
	}
	detail := buildDyn(t, reg, ".lq.GameDetailRecords", map[string]any{"version": 210715})
	list := detail.Mutable(detail.Descriptor().Fields().ByName("actions")).List()
	for _, s := range steps {
		action := buildDyn(t, reg, ".lq.GameAction", map[string]any{"type": 1,
			"result": wrapLogin(t, r, s.name, mustMarshal(t, buildDyn(t, reg, s.name, s.fields)))})
		list.Append(protoreflect.ValueOfMessage(action))
	}
	res := buildDyn(t, reg, ".lq.ResGameRecord", map[string]any{
		"data": wrapLogin(t, r, ".lq.GameDetailRecords", mustMarshal(t, detail)),
		"head": map[string]any{"uuid": uuid, "start_time": 111, "end_time": 222,
			"accounts": []map[string]any{{"account_id": 222222, "seat": 3}}},
	})
	return mustMarshal(t, res)
}

func offlineSeerResponse(t *testing.T, r *liqi.Resource, uuid string) []byte {
	t.Helper()
	report := map[string]any{"uuid": uuid,
		"events": []map[string]any{
			{"record_index": 0, "seer_index": 2, "recommends": []map[string]any{
				{"seat": 1, "predictions": []map[string]any{{"action": 139, "score": 80}, {"action": 111, "score": 15}}}}},
			{"record_index": 2, "seer_index": 4, "recommends": []map[string]any{
				{"seat": 2, "predictions": []map[string]any{{"action": 147, "score": 70}, {"action": 141, "score": 20}}}}},
			{"record_index": 4, "seer_index": 6, "recommends": []map[string]any{
				{"seat": 3, "predictions": []map[string]any{{"action": 219, "score": 60}, {"action": 119, "score": 30}}}}},
			{"record_index": 8, "seer_index": 10, "recommends": []map[string]any{
				{"seat": 0, "predictions": []map[string]any{{"action": 147, "score": 90}}}}},
		},
		"rounds": []map[string]any{
			{"chang": 1, "ju": 1, "ben": 0, "player_scores": []map[string]any{
				{"seat": 0, "rating": 44}, {"seat": 1, "rating": 56}, {"seat": 2, "rating": 16}, {"seat": 3, "rating": 5}}},
		}}
	res := buildDyn(t, r.Registry, ".lq.ResFetchSeerReport", map[string]any{"report": report})
	return mustMarshal(t, res)
}

// offlineCaptureFrames returns the full synthetic exchange: login (request
// number 1), game record (2), MAKA report (3).
func offlineCaptureFrames(t *testing.T, r *liqi.Resource, uuid string) []wsFrame {
	t.Helper()
	loginReq, loginRes := loginFrames(t, r, 222222)
	return []wsFrame{
		{"sent", loginReq},
		{"received", loginRes},
		{"sent", rpcFrame(2, 2, wrapLogin(t, r, fetchGameRecordMethod, nil))},
		{"received", rpcFrame(3, 2, wrapLogin(t, r, "", offlineRecordResponse(t, r, uuid)))},
		{"sent", rpcFrame(2, 3, wrapLogin(t, r, fetchSeerReportMethod, nil))},
		{"received", rpcFrame(3, 3, wrapLogin(t, r, "", offlineSeerResponse(t, r, uuid)))},
	}
}

func TestOfflineDecodeCLIEndToEnd(t *testing.T) {
	metaPath, profilePath, r := offlineEvidence(t)
	const uuid = "synthetic-uuid"
	dir := t.TempDir()
	capturePath := filepath.Join(dir, "capture.jsonl")
	writeWSCapture(t, capturePath, offlineCaptureFrames(t, r, uuid))
	gamesDir := filepath.Join(dir, "games")
	outPath := filepath.Join(dir, "out.json")

	var errBuf bytes.Buffer
	code := runDecodeWith([]string{"--liqi-meta", metaPath, "--protocol", profilePath, "--games-dir", gamesDir, "--out", outPath, capturePath}, &errBuf, nil)
	if code != 0 {
		t.Fatalf("decode failed (%d): %s", code, errBuf.String())
	}

	storedPath := filepath.Join(gamesDir, uuid+".json")
	if info, err := os.Stat(storedPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("stored game not private: %v %v", info, err)
	}
	stored, err := os.ReadFile(storedPath)
	if err != nil {
		t.Fatalf("stored game missing: %v", err)
	}
	var doc struct {
		SchemaVersion int       `json:"schema_version"`
		CapturedAt    time.Time `json:"captured_at"`
		Game          struct {
			UUID      string   `json:"game_uuid"`
			StartTime uint64   `json:"start_time"`
			EndTime   uint64   `json:"end_time"`
			SelfSeat  *int     `json:"self_seat"`
			MakaUUID  string   `json:"maka_uuid"`
			Version   uint64   `json:"record_version"`
			Issues    []string `json:"issues"`
			Rounds    []struct {
				Decisions []struct {
					ActionIndex int    `json:"action_index"`
					Discard     string `json:"discard"`
					Maka        *struct {
						BestScore        int64  `json:"best_score"`
						ScoreDeltaVsBest *int64 `json:"score_delta_vs_best"`
						Candidates       []struct {
							Tile string `json:"tile"`
						} `json:"candidates"`
					} `json:"maka"`
				} `json:"decisions"`
			} `json:"rounds"`
		} `json:"game"`
	}
	if err := json.Unmarshal(stored, &doc); err != nil {
		t.Fatalf("stored game unreadable: %v\n%s", err, stored)
	}
	// The store keeps its schema version and exactly the game record
	// response event's timestamp (seq 4 in offlineCaptureFrames) — not the
	// decode clock and not another event's stamp.
	if doc.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", doc.SchemaVersion)
	}
	if want := captureEventTime(4); !doc.CapturedAt.Equal(want) {
		t.Fatalf("captured_at %v, want the game response event time %v", doc.CapturedAt, want)
	}
	g := doc.Game
	if g.UUID != uuid || g.StartTime != 111 || g.EndTime != 222 || g.MakaUUID != uuid || g.Version != 210715 || len(g.Issues) != 0 {
		t.Fatalf("game header mismatch: %+v", g)
	}
	if g.SelfSeat == nil || *g.SelfSeat != 3 {
		t.Fatalf("self seat not resolved from login: %v", g.SelfSeat)
	}
	if len(g.Rounds) != 1 || len(g.Rounds[0].Decisions) != 5 {
		t.Fatalf("rounds/decisions mismatch: %+v", g.Rounds)
	}
	joined := 0
	for _, d := range g.Rounds[0].Decisions {
		if d.Maka != nil {
			joined++
		}
		if d.ActionIndex == 1 {
			if d.Discard != "9s" || d.Maka == nil || d.Maka.BestScore != 80 ||
				d.Maka.ScoreDeltaVsBest == nil || *d.Maka.ScoreDeltaVsBest != 0 ||
				len(d.Maka.Candidates) != 2 || d.Maka.Candidates[0].Tile != "9s" {
				t.Fatalf("decision 1 join mismatch: %+v", d)
			}
		}
		if d.ActionIndex == 11 && d.Maka != nil {
			t.Fatalf("post-riichi discard must stay unjoined: %+v", d)
		}
	}
	if joined != 4 {
		t.Fatalf("maka joined decisions = %d, want 4", joined)
	}

	if info, err := os.Stat(outPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("--out not private: %v %v", info, err)
	}
	outData, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("--out missing: %v", err)
	}
	var outGames []struct {
		UUID string `json:"game_uuid"`
	}
	if err := json.Unmarshal(outData, &outGames); err != nil || len(outGames) != 1 || outGames[0].UUID != uuid {
		t.Fatalf("--out content mismatch: %v %s", err, outData)
	}
	if outData[len(outData)-1] != '\n' {
		t.Fatal("--out must end with a newline")
	}

	// The stored game object and --out entry are the same normalized game.
	var storedDoc struct {
		Game any `json:"game"`
	}
	var outAny []any
	if err := json.Unmarshal(stored, &storedDoc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(outData, &outAny); err != nil || len(outAny) != 1 {
		t.Fatalf("--out shape: %v", err)
	}
	if !reflect.DeepEqual(storedDoc.Game, outAny[0]) {
		t.Fatalf("stored game and --out entry differ:\n%v\n%v", storedDoc.Game, outAny[0])
	}

	// No player identifiers reach the normal logs or the normalized JSON:
	// the synthetic account id 222222 exists only inside the raw capture.
	for name, blob := range map[string][]byte{"logs": errBuf.Bytes(), "stored": stored, "out": outData} {
		for _, banned := range []string{"222222", "account_id", "nickname"} {
			if bytes.Contains(blob, []byte(banned)) {
				t.Fatalf("%s contains %q:\n%s", name, banned, blob)
			}
		}
	}

	// --out must never overwrite an existing file, and a rejected run must
	// leave the existing bytes untouched.
	errBuf.Reset()
	if code := runDecodeWith([]string{"--liqi-meta", metaPath, "--protocol", profilePath, "--out", outPath, capturePath}, &errBuf, nil); code != 1 {
		t.Fatalf("existing --out overwritten (code %d): %s", code, errBuf.String())
	}
	after, err := os.ReadFile(outPath)
	if err != nil || !bytes.Equal(after, outData) {
		t.Fatalf("--out bytes changed after rejected run: %v", err)
	}

	// UUID filter that matches nothing fails with the original message.
	errBuf.Reset()
	if code := runDecodeWith([]string{"--liqi-meta", metaPath, "--protocol", profilePath, "--game-uuid", "other-uuid-000", capturePath}, &errBuf, nil); code != 1 || !bytes.Contains(errBuf.Bytes(), []byte("no matching game record")) {
		t.Fatalf("uuid filter mismatch (code %d): %s", code, errBuf.String())
	}
}

func TestOfflineDecodeCLIFailures(t *testing.T) {
	metaPath, profilePath, r := offlineEvidence(t)
	dir := t.TempDir()

	// Missing capture keeps exit 1 and the original open-capture message.
	var errBuf bytes.Buffer
	if code := runDecodeWith([]string{"--liqi-meta", metaPath, "--protocol", profilePath, filepath.Join(dir, "missing.jsonl")}, &errBuf, nil); code != 1 || !bytes.Contains(errBuf.Bytes(), []byte("open capture")) {
		t.Fatalf("missing capture (code %d): %s", code, errBuf.String())
	}

	// A capture bound to a different schema fails during replay.
	mismatched := filepath.Join(dir, "mismatched.jsonl")
	ctx, err := json.Marshal(map[string]any{"liqi": liqi.Metadata{GameVersion: "other", ResourceVersion: "other", SHA256: "other"}})
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(capture.Event{SchemaVersion: 1, Seq: 1, CapturedAt: time.Now(), Kind: "capture_context", Details: ctx})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mismatched, append(line, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	errBuf.Reset()
	if code := runDecodeWith([]string{"--liqi-meta", metaPath, "--protocol", profilePath, mismatched}, &errBuf, nil); code != 1 || !bytes.Contains(errBuf.Bytes(), []byte("decode capture")) {
		t.Fatalf("binding mismatch (code %d): %s", code, errBuf.String())
	}

	// A truncated capture fails replay instead of being half-read.
	broken := filepath.Join(dir, "broken.jsonl")
	if err := os.WriteFile(broken, []byte(`{"schema_version":1,"seq":1`), 0600); err != nil {
		t.Fatal(err)
	}
	errBuf.Reset()
	if code := runDecodeWith([]string{"--liqi-meta", metaPath, "--protocol", profilePath, broken}, &errBuf, nil); code != 1 || !bytes.Contains(errBuf.Bytes(), []byte("decode capture")) {
		t.Fatalf("broken capture (code %d): %s", code, errBuf.String())
	}

	// When a later game fails to store, the earlier stores must survive.
	good, bad := "synthetic-uuid", "x" // "x" fails the store uuid pattern
	partial := filepath.Join(dir, "partial.jsonl")
	frames := []wsFrame{
		{"sent", rpcFrame(2, 2, wrapLogin(t, r, fetchGameRecordMethod, nil))},
		{"received", rpcFrame(3, 2, wrapLogin(t, r, "", offlineRecordResponse(t, r, good)))},
		{"sent", rpcFrame(2, 3, wrapLogin(t, r, fetchGameRecordMethod, nil))},
		{"received", rpcFrame(3, 3, wrapLogin(t, r, "", offlineRecordResponse(t, r, bad)))},
	}
	writeWSCapture(t, partial, frames)
	gamesDir := filepath.Join(dir, "games")
	errBuf.Reset()
	if code := runDecodeWith([]string{"--liqi-meta", metaPath, "--protocol", profilePath, "--games-dir", gamesDir, partial}, &errBuf, nil); code != 1 || !bytes.Contains(errBuf.Bytes(), []byte("store game")) {
		t.Fatalf("later store failure (code %d): %s", code, errBuf.String())
	}
	if _, err := os.Stat(filepath.Join(gamesDir, good+".json")); err != nil {
		t.Fatalf("earlier stored game lost after later failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(gamesDir, bad+".json")); err == nil {
		t.Fatal("invalid uuid unexpectedly stored")
	}
}

// TestOfflineEvidenceRoundTrip guards the fixture invariant the other tests
// rely on: frames built from the in-memory resource decode under the
// evidence files written to disk.
func TestOfflineEvidenceRoundTrip(t *testing.T) {
	metaPath, _, r := offlineEvidence(t)
	loaded, err := liqi.OpenCachedMetadata(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Metadata.SHA256 != r.Metadata.SHA256 || loaded.Metadata.GameVersion != r.Metadata.GameVersion {
		t.Fatalf("evidence round trip mismatch: %+v vs %+v", loaded.Metadata, r.Metadata)
	}
}

// TestOfflineDecodeOrderIndependence verifies that reports and logins are
// collected from the whole capture before joining: report -> game -> login
// produces the same normalized game (self seat, MAKA join included) as
// login -> game -> report.
func TestOfflineDecodeOrderIndependence(t *testing.T) {
	metaPath, profilePath, r := offlineEvidence(t)
	const uuid = "synthetic-uuid"
	loginReq, loginRes := loginFrames(t, r, 222222)
	reversed := []wsFrame{
		{"sent", rpcFrame(2, 4, wrapLogin(t, r, fetchSeerReportMethod, nil))},
		{"received", rpcFrame(3, 4, wrapLogin(t, r, "", offlineSeerResponse(t, r, uuid)))},
		{"sent", rpcFrame(2, 5, wrapLogin(t, r, fetchGameRecordMethod, nil))},
		{"received", rpcFrame(3, 5, wrapLogin(t, r, "", offlineRecordResponse(t, r, uuid)))},
		{"sent", loginReq},
		{"received", loginRes},
	}
	decodeGame := func(name string, frames []wsFrame) map[string]any {
		dir := t.TempDir()
		capturePath := filepath.Join(dir, name+".jsonl")
		writeWSCapture(t, capturePath, frames)
		gamesDir := filepath.Join(dir, "games")
		var errBuf bytes.Buffer
		if code := runDecodeWith([]string{"--liqi-meta", metaPath, "--protocol", profilePath, "--games-dir", gamesDir, capturePath}, &errBuf, nil); code != 0 {
			t.Fatalf("%s decode failed (%d): %s", name, code, errBuf.String())
		}
		raw, err := os.ReadFile(filepath.Join(gamesDir, uuid+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		game, ok := doc["game"].(map[string]any)
		if !ok {
			t.Fatalf("%s stored file has no game object", name)
		}
		return game
	}
	standard := decodeGame("standard", offlineCaptureFrames(t, r, uuid))
	swapped := decodeGame("reversed", reversed)
	if !reflect.DeepEqual(standard, swapped) {
		t.Fatalf("game differs by capture order:\nstandard: %v\nreversed: %v", standard, swapped)
	}
	// Sanity: the shared result actually carries the join and self seat.
	if swapped["self_seat"] != float64(3) || swapped["maka_uuid"] != uuid {
		t.Fatalf("reversed order lost self seat or MAKA join: %v", swapped)
	}
}
