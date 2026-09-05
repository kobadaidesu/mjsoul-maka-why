package extract

import (
	"os"
	"reflect"
	"testing"

	"mjcap/internal/decode"
	"mjcap/internal/liqi"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func registry(t *testing.T) *liqi.Registry {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/liqi/gamerecord.json")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := liqi.Build(raw)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func build(t *testing.T, reg *liqi.Registry, name string, fields map[string]any) decode.ActionRecord {
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
		case []map[string]any:
			list := m.Mutable(fd).List()
			for _, child := range v {
				sub := build(t, reg, "."+string(fd.Message().FullName()), child)
				list.Append(protoreflect.ValueOfMessage(sub.Message))
			}
		default:
			t.Fatalf("unsupported fixture value %T", v)
		}
	}
	return decode.ActionRecord{Type: 1, Name: name, Status: "decoded", Message: m}
}

func newRoundAction(t *testing.T, reg *liqi.Registry, tiles [][]string) decode.ActionRecord {
	t.Helper()
	fields := map[string]any{
		"chang": 1, "ju": 1, "ben": 0,
		"scores": []int32{25000, 25000, 25000, 25000},
		"doras":  []string{"4p"},
	}
	names := []string{"tiles0", "tiles1", "tiles2", "tiles3"}
	for i, hand := range tiles {
		fields[names[i]] = hand
	}
	return build(t, reg, ".lq.RecordNewRound", fields)
}

func fourHands() [][]string {
	return [][]string{
		{"1m", "2m", "3m", "5z", "5z", "5z", "7p", "8p", "9p", "1s", "2s", "3s", "6z"},
		{"1m", "1m", "2p", "3p", "4p", "5p", "6p", "7p", "1s", "2s", "3s", "7z", "7z", "9s"},
		{"1p", "2p", "3p", "4p", "5p", "6p", "7p", "8p", "9p", "1m", "2m", "3m", "7z"},
		{"4m", "5m", "9m", "9m", "1p", "1p", "2s", "2s", "3z", "3z", "4z", "4z", "6z"},
	}
}

func TestRoundsReconstruction(t *testing.T) {
	reg := registry(t)
	actions := []decode.ActionRecord{
		newRoundAction(t, reg, fourHands()),
		build(t, reg, ".lq.RecordDiscardTile", map[string]any{"seat": 1, "tile": "9s"}),
		build(t, reg, ".lq.RecordDealTile", map[string]any{"seat": 2, "tile": "1z"}),
		build(t, reg, ".lq.RecordDiscardTile", map[string]any{"seat": 2, "tile": "1z", "moqie": true}),
		build(t, reg, ".lq.RecordChiPengGang", map[string]any{"seat": 3, "tiles": []string{"4m", "5m", "3m"}, "froms": []uint32{3, 3, 2}}),
		build(t, reg, ".lq.RecordDiscardTile", map[string]any{"seat": 3, "tile": "9m"}),
		build(t, reg, ".lq.RecordDealTile", map[string]any{"seat": 0, "tile": "5z"}),
		build(t, reg, ".lq.RecordAnGangAddGang", map[string]any{"seat": 0, "tiles": "5z", "doras": []string{"4p", "8s"}}),
		build(t, reg, ".lq.RecordDealTile", map[string]any{"seat": 0, "tile": "9p"}),
		build(t, reg, ".lq.RecordDiscardTile", map[string]any{"seat": 0, "tile": "1m", "is_liqi": true}),
		build(t, reg, ".lq.RecordHule", map[string]any{"scores": []int32{26000, 27000, 24000, 23000}}),
	}
	for i := range actions {
		actions[i].Index = i
	}
	game, err := Rounds("synthetic-uuid", decode.GameDetail{Version: 1, Actions: actions})
	if err != nil {
		t.Fatal(err)
	}
	if game.Seats != 4 || len(game.Rounds) != 1 {
		t.Fatalf("game %+v", game)
	}
	r := game.Rounds[0]
	if len(r.Issues) != 0 {
		t.Fatalf("unexpected issues %v", r.Issues)
	}
	if r.Label != "南2局0本場" || r.Wind != "south" || r.Kyoku != 2 || r.KyokuIndex != 5 || r.HandIndex != 0 || r.DealerSeat != 1 {
		t.Fatalf("round identity %+v", r)
	}
	if r.EndStatus != "hule" || !reflect.DeepEqual(r.EndScores, []int64{26000, 27000, 24000, 23000}) {
		t.Fatalf("round end %+v", r)
	}
	if !reflect.DeepEqual(r.DoraIndicators, []string{"4p", "8s"}) {
		t.Fatalf("kan dora not applied: %v", r.DoraIndicators)
	}
	if len(r.Decisions) != 4 {
		t.Fatalf("decisions %+v", r.Decisions)
	}
	dealer := r.Decisions[0]
	if dealer.Seat != 1 || dealer.TurnIndex != 1 || dealer.Draw != "" || dealer.Discard != "9s" || len(dealer.HandBefore) != 14 {
		t.Fatalf("dealer decision %+v", dealer)
	}
	tsumogiri := r.Decisions[1]
	if tsumogiri.Seat != 2 || !tsumogiri.Tsumogiri || tsumogiri.Draw != "1z" || len(tsumogiri.HandBefore) != 14 {
		t.Fatalf("tsumogiri decision %+v", tsumogiri)
	}
	afterCall := r.Decisions[2]
	if afterCall.Seat != 3 || afterCall.Draw != "" || len(afterCall.HandBefore) != 11 {
		t.Fatalf("post-call decision %+v", afterCall)
	}
	riichi := r.Decisions[3]
	// 13 dealt - 3 closed-kan copies (the fourth was the draw) + rinshan draw.
	if riichi.Seat != 0 || !riichi.Riichi || riichi.Draw != "9p" || len(riichi.HandBefore) != 11 {
		t.Fatalf("riichi decision %+v", riichi)
	}
	if !sortedStrings(riichi.HandBefore) {
		t.Fatalf("hand snapshot not sorted: %v", riichi.HandBefore)
	}
}

func TestRoundsRecordsInconsistencies(t *testing.T) {
	reg := registry(t)
	actions := []decode.ActionRecord{
		newRoundAction(t, reg, fourHands()),
		build(t, reg, ".lq.RecordDiscardTile", map[string]any{"seat": 2, "tile": "9z"}),
		build(t, reg, ".lq.RecordDiscardTile", map[string]any{"seat": 2, "tile": "5s"}),
		build(t, reg, ".lq.RecordAnGangAddGang", map[string]any{"seat": 2, "tiles": "7z"}),
		{Name: ".lq.RecordFutureThing", Status: "decoded"},
		build(t, reg, ".lq.RecordNoTile", nil),
	}
	actions[4].Message = actions[1].Message
	for i := range actions {
		actions[i].Index = i
	}
	game, err := Rounds("synthetic-uuid", decode.GameDetail{Version: 1, Actions: actions})
	if err != nil {
		t.Fatal(err)
	}
	r := game.Rounds[0]
	// Both impossible discards and the unmeasured action name all surface.
	if len(r.Issues) != 3 {
		t.Fatalf("issues %v", r.Issues)
	}
	if len(r.Decisions) != 0 {
		t.Fatalf("inconsistent discards produced decisions: %+v", r.Decisions)
	}
	if r.EndStatus != "no_tile" {
		t.Fatalf("end %+v", r)
	}
}

func TestRoundsKakanAndRenchan(t *testing.T) {
	reg := registry(t)
	hands := fourHands()
	actions := []decode.ActionRecord{
		newRoundAction(t, reg, hands),
		// Added kan: exactly one copy of 6z in hand must be removed.
		build(t, reg, ".lq.RecordAnGangAddGang", map[string]any{"seat": 0, "tiles": "6z"}),
		build(t, reg, ".lq.RecordDealTile", map[string]any{"seat": 0, "tile": "9s"}),
		build(t, reg, ".lq.RecordDiscardTile", map[string]any{"seat": 0, "tile": "9s", "moqie": true}),
		newRoundAction(t, reg, hands),
		build(t, reg, ".lq.RecordHule", map[string]any{"scores": []int32{1, 2, 3, 4}}),
	}
	for i := range actions {
		actions[i].Index = i
	}
	game, err := Rounds("synthetic-uuid", decode.GameDetail{Version: 1, Actions: actions})
	if err != nil {
		t.Fatal(err)
	}
	if len(game.Rounds) != 2 {
		t.Fatalf("rounds %+v", game.Rounds)
	}
	first := game.Rounds[0]
	if len(first.Issues) != 0 || first.EndStatus != "interrupted_by_new_round" || first.HandIndex != 0 {
		t.Fatalf("first round %+v", first)
	}
	// 13 dealt - 1 kakan copy + 1 draw = 13 before the discard.
	if len(first.Decisions) != 1 || len(first.Decisions[0].HandBefore) != 13 {
		t.Fatalf("kakan accounting %+v", first.Decisions)
	}
	if game.Rounds[1].HandIndex != 1 || game.Rounds[1].EndStatus != "hule" {
		t.Fatalf("second round %+v", game.Rounds[1])
	}
}

func TestRoundsRejectsAmbiguousDeal(t *testing.T) {
	reg := registry(t)
	hands := fourHands()
	hands[0] = append(hands[0], "9s") // two seats now hold 14 tiles
	if _, err := Rounds("u", decode.GameDetail{Version: 1, Actions: []decode.ActionRecord{newRoundAction(t, reg, hands)}}); err == nil {
		t.Fatal("two dealers accepted")
	}
}

func sortedStrings(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}
