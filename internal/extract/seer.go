package extract

import (
	"fmt"

	"mjcap/internal/decode"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Everything in this file follows measurements recorded in
// docs/protocol-findings.md#phase3-maka-join. In the measured report:
//   - SeerEvent.record_index indexes the type=1 subsequence of
//     GameDetailRecords.actions and names the game event that opened the
//     decision (own draw, own call, round start, or an opponent's discard for
//     call opportunities).
//   - Discard-decision events resolve to the seat's next RecordDiscardTile or
//     RecordAnGangAddGang before the round ends.
//   - Prediction actions 110+10*suit+rank (suit m,p,s,z; rank 0 = red five)
//     are plain discards and 210+10*suit+rank are riichi discards; both were
//     verified against every reconstructed hand. Other values (1 = observed
//     on call opportunities, 6 = observed once as the chosen kan) stay raw.
const (
	seerActionDiscardBase = 110
	seerActionRiichiBase  = 210
)

// SeerCandidate is one MAKA prediction. Tile stays empty when the action
// value is outside the measured discard/riichi ranges; Kind stays empty for
// action values whose meaning has not been measured.
type SeerCandidate struct {
	Action int64  `json:"action"`
	Score  int64  `json:"score"`
	Tile   string `json:"tile,omitempty"`
	Riichi bool   `json:"riichi,omitempty"`
	Kind   string `json:"kind,omitempty"`
}

// Measured call/decision action values
// (docs/protocol-findings.md#call-actions): every executed call of the
// captured game matched these, and every prediction carrying them was
// feasible against the reconstructed hand (86/86).
var seerActionKinds = map[int64]string{
	1: "pass",
	2: "chi_low",
	3: "chi_mid",
	4: "chi_high",
	5: "pon",
	6: "kan",
	7: "win",
}

// MakaEval attaches the MAKA candidates for one discard decision. Scores were
// measured as "higher = stronger recommendation" (candidates arrive sorted by
// descending score and the client highlights the top one), so
// ScoreDeltaVsBest is 0 for the best choice and grows with the gap. The delta
// is omitted when the actual choice is not among the reported candidates.
type MakaEval struct {
	RecordIndex      int64           `json:"record_index"`
	SeerIndex        int64           `json:"seer_index"`
	Candidates       []SeerCandidate `json:"candidates"`
	BestScore        int64           `json:"best_score"`
	ActualScore      *int64          `json:"actual_score,omitempty"`
	ScoreDeltaVsBest *int64          `json:"score_delta_vs_best,omitempty"`
}

// SeerSideEval is a MAKA event that does not resolve to a tile discard: a
// call opportunity on an opponent's discard, or a decision answered with a
// kan. Candidates keep their raw action values.
type SeerSideEval struct {
	Kind               string          `json:"kind"`
	RecordIndex        int64           `json:"record_index"`
	SeerIndex          int64           `json:"seer_index"`
	TriggerActionIndex int             `json:"trigger_action_index"`
	Seat               int             `json:"seat"`
	Candidates         []SeerCandidate `json:"candidates"`
}

// SeerRating is one seat's per-round rating with its raw encoding; the
// rating-to-grade rule shown by the client is not yet measured.
type SeerRating struct {
	Seat   int    `json:"seat"`
	Rating uint64 `json:"rating"`
}

// tileFromSeerAction maps a measured discard/riichi action value to its tile
// code. It returns "" for values outside the verified ranges.
func tileFromSeerAction(action int64) (tile string, riichi bool) {
	base := action - seerActionDiscardBase
	if action >= seerActionRiichiBase {
		base = action - seerActionRiichiBase
		riichi = true
	}
	if base < 0 || base > 37 {
		return "", false
	}
	suit, rank := base/10, base%10
	if suit == 3 && (rank == 0 || rank > 7) {
		return "", false
	}
	return fmt.Sprintf("%d%c", rank, "mpsz"[suit]), riichi
}

// JoinSeer attaches a decoded ResFetchSeerReport to a reconstructed game.
// The report must belong to the same game uuid. Events that cannot be joined
// are recorded as issues, never guessed at.
func JoinSeer(game *Game, detail decode.GameDetail, report protoreflect.Message) error {
	uuid, err := decode.StringField(report, "uuid")
	if err != nil {
		return err
	}
	if uuid != game.UUID {
		return fmt.Errorf("seer report is for a different game record")
	}
	game.MakaUUID = uuid

	// The measured index space: type=1 actions in order.
	var typeOne []int
	for _, a := range detail.Actions {
		if a.Type == 1 {
			typeOne = append(typeOne, a.Index)
		}
	}
	nameAt := map[int]decode.ActionRecord{}
	for _, a := range detail.Actions {
		nameAt[a.Index] = a
	}
	decisions := map[int]*Decision{}
	roundOf := func(fullIndex int) *Round {
		for i := range game.Rounds {
			r := &game.Rounds[i]
			next := len(detail.Actions)
			if i+1 < len(game.Rounds) {
				next = game.Rounds[i+1].FirstAction
			}
			if fullIndex >= r.FirstAction && fullIndex < next {
				return r
			}
		}
		return nil
	}
	for i := range game.Rounds {
		for j := range game.Rounds[i].Decisions {
			d := &game.Rounds[i].Decisions[j]
			decisions[d.ActionIndex] = d
		}
	}

	if err := joinRatings(game, report); err != nil {
		return err
	}

	events, err := decode.MessagesField(report, "events")
	if err != nil {
		return err
	}
	for _, ev := range events {
		recordIndex, err := decode.IntField(ev, "record_index")
		if err != nil {
			return err
		}
		seerIndex, err := decode.IntField(ev, "seer_index")
		if err != nil {
			return err
		}
		recommends, err := decode.MessagesField(ev, "recommends")
		if err != nil {
			return err
		}
		if int(recordIndex) < 0 || int(recordIndex) >= len(typeOne) {
			game.Issues = append(game.Issues, fmt.Sprintf("seer event record_index %d outside the type=1 action range", recordIndex))
			continue
		}
		trigger := nameAt[typeOne[recordIndex]]
		round := roundOf(trigger.Index)
		if round == nil {
			game.Issues = append(game.Issues, fmt.Sprintf("seer event record_index %d outside any round", recordIndex))
			continue
		}
		// An opponent's discard may open call decisions for several seats at
		// once, so every recommend joins independently.
		for _, recommend := range recommends {
			seat, err := decode.IntField(recommend, "seat")
			if err != nil {
				return err
			}
			candidates, err := seerCandidates(recommend)
			if err != nil {
				return err
			}
			if trigger.Name == actionDiscardTile {
				round.SeerSideEvals = append(round.SeerSideEvals, SeerSideEval{
					Kind: "call_opportunity", RecordIndex: recordIndex, SeerIndex: seerIndex,
					TriggerActionIndex: trigger.Index, Seat: int(seat), Candidates: candidates,
				})
				continue
			}
			resolved := resolveDecision(detail, typeOne, nameAt, int(recordIndex), int(seat))
			switch {
			case resolved.kind != "":
				round.SeerSideEvals = append(round.SeerSideEvals, SeerSideEval{
					Kind: resolved.kind, RecordIndex: recordIndex, SeerIndex: seerIndex,
					TriggerActionIndex: trigger.Index, Seat: int(seat), Candidates: candidates,
				})
			case resolved.discard >= 0:
				d := decisions[resolved.discard]
				if d == nil {
					round.Issues = append(round.Issues, fmt.Sprintf("seer event record_index %d resolves to action %d with no reconstructed decision", recordIndex, resolved.discard))
					continue
				}
				d.Maka = makaEval(recordIndex, seerIndex, candidates, d)
			default:
				round.Issues = append(round.Issues, fmt.Sprintf("seer event record_index %d has no same-seat discard, kan or win before the round ends", recordIndex))
			}
		}
	}
	return nil
}

type resolvedDecision struct {
	discard int
	kind    string
}

// resolveDecision finds the seat's answer to a decision-opening event: its
// next RecordDiscardTile, RecordAnGangAddGang, or a RecordHule win before the
// round boundary (all three answers were observed in the measured capture).
func resolveDecision(detail decode.GameDetail, typeOne []int, nameAt map[int]decode.ActionRecord, recordIndex, seat int) resolvedDecision {
	for k := recordIndex + 1; k < len(typeOne); k++ {
		a := nameAt[typeOne[k]]
		if a.Name == actionNewRound {
			break
		}
		if a.Message == nil {
			continue
		}
		switch a.Name {
		case actionHule:
			hules, err := decode.MessagesField(a.Message, "hules")
			if err != nil {
				continue
			}
			for _, h := range hules {
				if s, err := decode.UintField(h, "seat"); err == nil && int(s) == seat {
					return resolvedDecision{discard: -1, kind: "win_decision"}
				}
				if s, err := decode.IntField(h, "seat"); err == nil && int(s) == seat {
					return resolvedDecision{discard: -1, kind: "win_decision"}
				}
			}
		case actionAnGangAddGang, actionDiscardTile:
			s, err := decode.UintField(a.Message, "seat")
			if err != nil || int(s) != seat {
				continue
			}
			if a.Name == actionAnGangAddGang {
				return resolvedDecision{discard: -1, kind: "kan_decision"}
			}
			return resolvedDecision{discard: a.Index}
		}
	}
	return resolvedDecision{discard: -1}
}

func seerCandidates(recommend protoreflect.Message) ([]SeerCandidate, error) {
	predictions, err := decode.MessagesField(recommend, "predictions")
	if err != nil {
		return nil, err
	}
	out := make([]SeerCandidate, 0, len(predictions))
	for _, p := range predictions {
		action, err := decode.IntField(p, "action")
		if err != nil {
			return nil, err
		}
		score, err := decode.IntField(p, "score")
		if err != nil {
			return nil, err
		}
		tile, riichi := tileFromSeerAction(action)
		kind := seerActionKinds[action]
		if tile != "" {
			kind = "discard"
			if riichi {
				kind = "riichi_discard"
			}
		}
		out = append(out, SeerCandidate{Action: action, Score: score, Tile: tile, Riichi: riichi, Kind: kind})
	}
	return out, nil
}

func makaEval(recordIndex, seerIndex int64, candidates []SeerCandidate, d *Decision) *MakaEval {
	eval := &MakaEval{RecordIndex: recordIndex, SeerIndex: seerIndex, Candidates: candidates}
	for _, c := range candidates {
		if c.Score > eval.BestScore {
			eval.BestScore = c.Score
		}
		if c.Tile == d.Discard && c.Riichi == d.Riichi && eval.ActualScore == nil {
			score := c.Score
			eval.ActualScore = &score
		}
	}
	if eval.ActualScore != nil {
		delta := eval.BestScore - *eval.ActualScore
		eval.ScoreDeltaVsBest = &delta
	}
	return eval
}

func joinRatings(game *Game, report protoreflect.Message) error {
	rounds, err := decode.MessagesField(report, "rounds")
	if err != nil {
		return err
	}
	for _, sr := range rounds {
		chang, err := decode.UintField(sr, "chang")
		if err != nil {
			return err
		}
		ju, err := decode.UintField(sr, "ju")
		if err != nil {
			return err
		}
		ben, err := decode.UintField(sr, "ben")
		if err != nil {
			return err
		}
		scores, err := decode.MessagesField(sr, "player_scores")
		if err != nil {
			return err
		}
		var target *Round
		for i := range game.Rounds {
			r := &game.Rounds[i]
			if r.KyokuIndex == int(chang)*4+int(ju) && r.Honba == int(ben) {
				target = r
				break
			}
		}
		if target == nil {
			game.Issues = append(game.Issues, fmt.Sprintf("seer round chang=%d ju=%d ben=%d has no reconstructed round", chang, ju, ben))
			continue
		}
		for _, s := range scores {
			seat, err := decode.UintField(s, "seat")
			if err != nil {
				return err
			}
			rating, err := decode.UintField(s, "rating")
			if err != nil {
				return err
			}
			target.MakaRatings = append(target.MakaRatings, SeerRating{Seat: int(seat), Rating: rating})
		}
	}
	return nil
}
