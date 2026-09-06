package extract

import (
	"testing"

	"mjcap/internal/decode"

	"mjcap/internal/liqi"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func seerReport(t *testing.T, reg *liqi.Registry, uuid string, events []map[string]any, rounds []map[string]any) protoreflect.Message {
	t.Helper()
	fields := map[string]any{"uuid": uuid}
	if events != nil {
		fields["events"] = events
	}
	if rounds != nil {
		fields["rounds"] = rounds
	}
	return build(t, reg, ".lq.SeerReport", fields).Message
}

func seerScenario(t *testing.T, reg *liqi.Registry) (Game, decode.GameDetail) {
	t.Helper()
	actions := []decode.ActionRecord{
		newRoundAction(t, reg, fourHands()),                                                                                                  // 0
		build(t, reg, ".lq.RecordDiscardTile", map[string]any{"seat": 1, "tile": "9s"}),                                                      // 1 decision A
		build(t, reg, ".lq.RecordDealTile", map[string]any{"seat": 2, "tile": "1z"}),                                                         // 2
		build(t, reg, ".lq.RecordDiscardTile", map[string]any{"seat": 2, "tile": "1z", "moqie": true}),                                       // 3 decision B
		build(t, reg, ".lq.RecordDealTile", map[string]any{"seat": 3, "tile": "9m"}),                                                         // 4
		build(t, reg, ".lq.RecordDiscardTile", map[string]any{"seat": 3, "tile": "9m", "is_liqi": true}),                                     // 5 decision C (riichi)
		build(t, reg, ".lq.RecordDealTile", map[string]any{"seat": 0, "tile": "4z"}),                                                         // 6
		build(t, reg, ".lq.RecordAnGangAddGang", map[string]any{"seat": 0, "tiles": "6z"}),                                                   // 7 added kan
		build(t, reg, ".lq.RecordDealTile", map[string]any{"seat": 0, "tile": "7z"}),                                                         // 8 rinshan
		build(t, reg, ".lq.RecordDiscardTile", map[string]any{"seat": 0, "tile": "7z", "moqie": true}),                                       // 9 decision D
		build(t, reg, ".lq.RecordDealTile", map[string]any{"seat": 3, "tile": "1s"}),                                                         // 10
		build(t, reg, ".lq.RecordDiscardTile", map[string]any{"seat": 3, "tile": "1s", "moqie": true}),                                       // 11 decision E (post-riichi)
		build(t, reg, ".lq.RecordDealTile", map[string]any{"seat": 2, "tile": "7z"}),                                                         // 12
		build(t, reg, ".lq.RecordHule", map[string]any{"hules": []map[string]any{{"seat": 2, "zimo": true}}, "scores": []int32{1, 2, 3, 4}}), // 13 win
	}
	for i := range actions {
		actions[i].Index = i
	}
	detail := decode.GameDetail{Version: 1, Actions: actions}
	game, err := Rounds("synthetic-uuid", detail)
	if err != nil {
		t.Fatal(err)
	}
	if issues := len(game.Issues) + len(game.Rounds[0].Issues); issues != 0 {
		t.Fatalf("scenario not clean: %v %v", game.Issues, game.Rounds[0].Issues)
	}
	return game, detail
}

func TestJoinSeer(t *testing.T) {
	reg := registry(t)
	game, detail := seerScenario(t, reg)
	report := seerReport(t, reg, "synthetic-uuid",
		[]map[string]any{
			{"record_index": 0, "seer_index": 2, "recommends": []map[string]any{
				{"seat": 1, "predictions": []map[string]any{{"action": 139, "score": 80}, {"action": 111, "score": 15}}}}},
			{"record_index": 1, "seer_index": 3, "recommends": []map[string]any{
				{"seat": 3, "predictions": []map[string]any{{"action": 1, "score": 99}}},
				{"seat": 0, "predictions": []map[string]any{{"action": 1, "score": 90}, {"action": 5, "score": 9}}}}},
			{"record_index": 2, "seer_index": 4, "recommends": []map[string]any{
				{"seat": 2, "predictions": []map[string]any{{"action": 147, "score": 70}, {"action": 141, "score": 20}}}}},
			{"record_index": 4, "seer_index": 6, "recommends": []map[string]any{
				{"seat": 3, "predictions": []map[string]any{{"action": 219, "score": 60}, {"action": 119, "score": 30}}}}},
			{"record_index": 6, "seer_index": 8, "recommends": []map[string]any{
				{"seat": 0, "predictions": []map[string]any{{"action": 6, "score": 97}, {"action": 146, "score": 2}}}}},
			{"record_index": 8, "seer_index": 10, "recommends": []map[string]any{
				{"seat": 0, "predictions": []map[string]any{{"action": 147, "score": 90}}}}},
			{"record_index": 12, "seer_index": 14, "recommends": []map[string]any{
				{"seat": 2, "predictions": []map[string]any{{"action": 7, "score": 99}}}}},
		},
		[]map[string]any{
			{"chang": 1, "ju": 1, "ben": 0, "player_scores": []map[string]any{
				{"seat": 0, "rating": 44}, {"seat": 1, "rating": 56}, {"seat": 2, "rating": 16}, {"seat": 3, "rating": 5}}},
		})
	if err := JoinSeer(&game, detail, report); err != nil {
		t.Fatal(err)
	}
	if len(game.Issues) != 0 || len(game.Rounds[0].Issues) != 0 {
		t.Fatalf("join issues: %v %v", game.Issues, game.Rounds[0].Issues)
	}
	r := game.Rounds[0]
	byAction := map[int]Decision{}
	for _, d := range r.Decisions {
		byAction[d.ActionIndex] = d
	}
	a := byAction[1].Maka
	if a == nil || *a.ScoreDeltaVsBest != 0 || a.BestScore != 80 || a.Candidates[0].Tile != "9s" || a.Candidates[1].Tile != "1m" {
		t.Fatalf("decision A join %+v", a)
	}
	b := byAction[3].Maka
	if b == nil || b.Candidates[0].Tile != "7z" || *b.ActualScore != 20 || *b.ScoreDeltaVsBest != 50 {
		t.Fatalf("decision B join %+v", b)
	}
	c := byAction[5].Maka
	if c == nil || !c.Candidates[0].Riichi || c.Candidates[0].Tile != "9m" || c.Candidates[1].Riichi || *c.ScoreDeltaVsBest != 0 {
		t.Fatalf("riichi decision join %+v", c)
	}
	d := byAction[9].Maka
	if d == nil || *d.ScoreDeltaVsBest != 0 || d.Candidates[0].Tile != "7z" {
		t.Fatalf("post-kan decision join %+v", d)
	}
	if byAction[11].Maka != nil {
		t.Fatalf("post-riichi forced discard must stay unjoined: %+v", byAction[11].Maka)
	}
	if c := r.SeerSideEvals; len(c) > 0 {
		for _, se := range c {
			for _, cand := range se.Candidates {
				switch cand.Action {
				case 1:
					if cand.Kind != "pass" {
						t.Fatalf("action 1 kind %q", cand.Kind)
					}
				case 5:
					if cand.Kind != "pon" {
						t.Fatalf("action 5 kind %q", cand.Kind)
					}
				case 6:
					if cand.Kind != "kan" {
						t.Fatalf("action 6 kind %q", cand.Kind)
					}
				case 7:
					if cand.Kind != "win" {
						t.Fatalf("action 7 kind %q", cand.Kind)
					}
				}
			}
		}
	}
	if a := byAction[1].Maka.Candidates[0]; a.Kind != "discard" {
		t.Fatalf("discard kind %q", a.Kind)
	}
	if c := byAction[5].Maka.Candidates[0]; c.Kind != "riichi_discard" {
		t.Fatalf("riichi kind %q", c.Kind)
	}
	kinds := map[string]int{}
	seats := map[string][]int{}
	for _, s := range r.SeerSideEvals {
		kinds[s.Kind]++
		seats[s.Kind] = append(seats[s.Kind], s.Seat)
	}
	if kinds["call_opportunity"] != 2 || kinds["kan_decision"] != 1 || kinds["win_decision"] != 1 {
		t.Fatalf("side evals %+v", r.SeerSideEvals)
	}
	if seats["call_opportunity"][0] != 3 || seats["call_opportunity"][1] != 0 {
		t.Fatalf("call seats %+v", seats)
	}
	if len(r.MakaRatings) != 4 || r.MakaRatings[2].Rating != 16 {
		t.Fatalf("ratings %+v", r.MakaRatings)
	}
	if game.MakaUUID != "synthetic-uuid" {
		t.Fatalf("maka uuid %q", game.MakaUUID)
	}
}

func TestJoinSeerRejectsOtherGame(t *testing.T) {
	reg := registry(t)
	game, detail := seerScenario(t, reg)
	report := seerReport(t, reg, "another-uuid", nil, nil)
	if err := JoinSeer(&game, detail, report); err == nil {
		t.Fatal("foreign seer report accepted")
	}
}

func TestJoinSeerRecordsUnjoinableEvents(t *testing.T) {
	reg := registry(t)
	game, detail := seerScenario(t, reg)
	report := seerReport(t, reg, "synthetic-uuid",
		[]map[string]any{
			{"record_index": 9999, "seer_index": 1, "recommends": []map[string]any{{"seat": 0}}},
			{"record_index": 10, "seer_index": 12, "recommends": []map[string]any{
				{"seat": 1, "predictions": []map[string]any{{"action": 111, "score": 50}}}}},
		}, nil)
	if err := JoinSeer(&game, detail, report); err != nil {
		t.Fatal(err)
	}
	if len(game.Issues) != 1 {
		t.Fatalf("out-of-range event not recorded: %v", game.Issues)
	}
	// record_index 10 opens seat 3's forced draw; seat 1 never answers it.
	if len(game.Rounds[0].Issues) != 1 {
		t.Fatalf("unresolvable event not recorded: %v", game.Rounds[0].Issues)
	}
}

func TestTileFromSeerAction(t *testing.T) {
	for _, tc := range []struct {
		action int64
		tile   string
		riichi bool
	}{
		{110, "0m", false}, {119, "9m", false}, {120, "0p", false}, {125, "5p", false},
		{130, "0s", false}, {141, "1z", false}, {147, "7z", false},
		{210, "0m", true}, {219, "9m", true}, {229, "9p", true}, {247, "7z", true},
		{140, "", false}, {148, "", false}, {109, "", false}, {240, "", false}, {248, "", false}, {1, "", false}, {7, "", false},
	} {
		tile, riichi := tileFromSeerAction(tc.action)
		if tile != tc.tile || (tile != "" && riichi != tc.riichi) {
			t.Fatalf("action %d -> %q riichi=%v", tc.action, tile, riichi)
		}
	}
}
