// Package extract converts decoded game-record actions into typed rounds and
// per-decision hand state. It reads seats and tile codes only; player names
// and account identifiers are never extracted here.
package extract

import (
	"fmt"
	"regexp"
	"sort"

	"mjcap/internal/decode"
)

// Tile codes were measured as strings like "4m", "0p" (red five) and "1z"
// (docs/protocol-findings.md#phase2-record-decode). Anything else is rejected
// loudly instead of being silently normalized.
var tilePattern = regexp.MustCompile(`^[0-9][mpsz]$`)

// Round holds one played hand. Index semantics follow the project constitution:
// HandIndex counts every played hand including repeats, KyokuIndex encodes
// wind*4+kyoku-1 and does not advance on honba repeats.
type Round struct {
	HandIndex      int        `json:"hand_index"`
	KyokuIndex     int        `json:"kyoku_index"`
	Wind           string     `json:"wind"`
	Kyoku          int        `json:"kyoku"`
	Honba          int        `json:"honba"`
	Label          string     `json:"label"`
	DealerSeat     int        `json:"dealer_seat"`
	ScoresStart    []int64    `json:"scores_start"`
	DoraIndicators []string   `json:"dora_indicators"`
	FirstAction    int        `json:"first_action_index"`
	Decisions      []Decision `json:"decisions"`
	EndStatus      string     `json:"end_status"`
	EndActionName  string     `json:"end_action_name,omitempty"`
	EndScores      []int64    `json:"end_scores,omitempty"`
	Issues         []string   `json:"issues,omitempty"`
}

// Decision is one discard decision. TurnIndex is this seat's 1-based discard
// count within the round; the MAKA join key is intentionally not fixed here.
type Decision struct {
	Seat        int      `json:"seat"`
	TurnIndex   int      `json:"turn_index"`
	ActionIndex int      `json:"action_index"`
	HandBefore  []string `json:"hand_before"`
	Draw        string   `json:"draw,omitempty"`
	Discard     string   `json:"discard"`
	Tsumogiri   bool     `json:"tsumogiri"`
	Riichi      bool     `json:"riichi"`
}

// Game is the Phase 2 reconstruction of one record. UUID identifies the game
// record itself; it carries no player identity.
type Game struct {
	UUID    string   `json:"game_uuid"`
	Version uint64   `json:"record_version"`
	Seats   int      `json:"seats"`
	Rounds  []Round  `json:"rounds"`
	Issues  []string `json:"issues,omitempty"`
}

// Measured action names (docs/protocol-findings.md#phase2-record-decode).
// Unlisted names are reported as issues, never guessed at.
const (
	actionNewRound      = ".lq.RecordNewRound"
	actionDealTile      = ".lq.RecordDealTile"
	actionDiscardTile   = ".lq.RecordDiscardTile"
	actionChiPengGang   = ".lq.RecordChiPengGang"
	actionAnGangAddGang = ".lq.RecordAnGangAddGang"
	actionHule          = ".lq.RecordHule"
	actionNoTile        = ".lq.RecordNoTile"
	actionLiuJu         = ".lq.RecordLiuJu"
	actionBaBei         = ".lq.RecordBaBei"
)

type seatState struct {
	hand     []string
	lastDraw string
	melds    int
}

// Rounds replays the decoded actions into typed rounds. Reconstruction is
// deterministic bookkeeping of measured events; any state the events cannot
// explain becomes an issue on the affected round instead of a silent guess.
func Rounds(uuid string, detail decode.GameDetail) (Game, error) {
	game := Game{UUID: uuid, Version: detail.Version}
	var round *Round
	var seats []seatState
	finish := func(status, name string) {
		if round != nil {
			round.EndStatus, round.EndActionName = status, name
			game.Rounds = append(game.Rounds, *round)
			round = nil
		}
	}
	for _, action := range detail.Actions {
		if action.Status == "no_result" {
			continue
		}
		if action.Status != "decoded" {
			game.Issues = append(game.Issues, fmt.Sprintf("action %d not decodable: %s (%s)", action.Index, action.Status, action.Name))
			continue
		}
		switch action.Name {
		case actionNewRound:
			finish("interrupted_by_new_round", "")
			r, s, err := newRound(action, len(game.Rounds))
			if err != nil {
				return game, fmt.Errorf("action %d: %w", action.Index, err)
			}
			round, seats = &r, s
		case actionDealTile, actionDiscardTile, actionChiPengGang, actionAnGangAddGang, actionBaBei:
			if round == nil {
				game.Issues = append(game.Issues, fmt.Sprintf("action %d (%s) outside any round", action.Index, action.Name))
				continue
			}
			if err := applyToRound(round, seats, action); err != nil {
				round.Issues = append(round.Issues, err.Error())
			}
		case actionHule:
			if round != nil {
				if scores, err := decode.IntsField(action.Message, "scores"); err == nil && len(scores) > 0 {
					round.EndScores = scores
				}
			}
			finish("hule", action.Name)
		case actionNoTile:
			finish("no_tile", action.Name)
		case actionLiuJu:
			finish("liuju", action.Name)
		default:
			if round != nil {
				round.Issues = append(round.Issues, fmt.Sprintf("action %d has unmeasured name %s; ignored for state", action.Index, action.Name))
			} else {
				game.Issues = append(game.Issues, fmt.Sprintf("action %d has unmeasured name %s outside any round", action.Index, action.Name))
			}
		}
	}
	finish("truncated", "")
	if len(game.Rounds) == 0 {
		return game, fmt.Errorf("no rounds reconstructed")
	}
	game.Seats = len(game.Rounds[0].ScoresStart)
	return game, nil
}

func newRound(action decode.ActionRecord, handIndex int) (Round, []seatState, error) {
	m := action.Message
	chang, err := decode.UintField(m, "chang")
	if err != nil {
		return Round{}, nil, err
	}
	ju, err := decode.UintField(m, "ju")
	if err != nil {
		return Round{}, nil, err
	}
	ben, err := decode.UintField(m, "ben")
	if err != nil {
		return Round{}, nil, err
	}
	scores, err := decode.IntsField(m, "scores")
	if err != nil {
		return Round{}, nil, err
	}
	doras, err := decode.StringsField(m, "doras")
	if err != nil {
		return Round{}, nil, err
	}
	// chang/ju against the round list shown by the client UI: chang=1,ju=1 was
	// rendered as 南2局 (docs/protocol-findings.md#phase1-live-capture).
	winds := []string{"east", "south", "west", "north"}
	labels := []string{"東", "南", "西", "北"}
	if chang > 3 || ju > 3 {
		return Round{}, nil, fmt.Errorf("round position chang=%d ju=%d outside the measured range", chang, ju)
	}
	round := Round{
		HandIndex:      handIndex,
		KyokuIndex:     int(chang)*4 + int(ju),
		Wind:           winds[chang],
		Kyoku:          int(ju) + 1,
		Honba:          int(ben),
		Label:          fmt.Sprintf("%s%d局%d本場", labels[chang], ju+1, ben),
		DealerSeat:     -1,
		ScoresStart:    scores,
		DoraIndicators: doras,
		FirstAction:    action.Index,
	}
	seats := make([]seatState, len(scores))
	for seat := range seats {
		tiles, err := decode.StringsField(m, fmt.Sprintf("tiles%d", seat))
		if err != nil {
			return Round{}, nil, err
		}
		for _, tile := range tiles {
			if !tilePattern.MatchString(tile) {
				return Round{}, nil, fmt.Errorf("seat %d dealt invalid tile code %q", seat, tile)
			}
		}
		seats[seat].hand = append([]string(nil), tiles...)
		// The dealer is identified by data, not rule: exactly one seat was dealt
		// a 14th tile in every measured round.
		if len(tiles) == 14 {
			if round.DealerSeat >= 0 {
				return Round{}, nil, fmt.Errorf("two seats dealt 14 tiles")
			}
			// Which of the 14 tiles is "the draw" is not measured, so the
			// dealer's first decision keeps an empty Draw.
			round.DealerSeat = seat
		} else if len(tiles) != 13 {
			return Round{}, nil, fmt.Errorf("seat %d dealt %d tiles", seat, len(tiles))
		}
	}
	if round.DealerSeat < 0 {
		return Round{}, nil, fmt.Errorf("no seat was dealt 14 tiles")
	}
	return round, seats, nil
}

func applyToRound(round *Round, seats []seatState, action decode.ActionRecord) error {
	m := action.Message
	seatNo, err := decode.UintField(m, "seat")
	if err != nil {
		return err
	}
	if int(seatNo) >= len(seats) {
		return fmt.Errorf("action %d seat %d out of range", action.Index, seatNo)
	}
	state := &seats[seatNo]
	switch action.Name {
	case actionDealTile:
		tile, err := decode.StringField(m, "tile")
		if err != nil {
			return err
		}
		if !tilePattern.MatchString(tile) {
			return fmt.Errorf("action %d dealt invalid tile code %q", action.Index, tile)
		}
		state.hand = append(state.hand, tile)
		state.lastDraw = tile
		if doras, err := decode.StringsField(m, "doras"); err == nil && len(doras) > 0 {
			round.DoraIndicators = doras
		}
	case actionDiscardTile:
		tile, err := decode.StringField(m, "tile")
		if err != nil {
			return err
		}
		moqie, err := decode.BoolField(m, "moqie")
		if err != nil {
			return err
		}
		liqi, err := decode.BoolField(m, "is_liqi")
		if err != nil {
			return err
		}
		wliqi, err := decode.BoolField(m, "is_wliqi")
		if err != nil {
			return err
		}
		hand := append([]string(nil), state.hand...)
		sort.Strings(hand)
		if !removeTile(&state.hand, tile) {
			return fmt.Errorf("action %d: seat %d discarded %s not present in reconstructed hand", action.Index, seatNo, tile)
		}
		turn := 1
		for _, d := range round.Decisions {
			if d.Seat == int(seatNo) {
				turn++
			}
		}
		round.Decisions = append(round.Decisions, Decision{
			Seat:        int(seatNo),
			TurnIndex:   turn,
			ActionIndex: action.Index,
			HandBefore:  hand,
			Draw:        state.lastDraw,
			Discard:     tile,
			Tsumogiri:   moqie,
			Riichi:      liqi || wliqi,
		})
		state.lastDraw = ""
		if doras, err := decode.StringsField(m, "doras"); err == nil && len(doras) > 0 {
			round.DoraIndicators = doras
		}
	case actionChiPengGang:
		tiles, err := decode.StringsField(m, "tiles")
		if err != nil {
			return err
		}
		froms, err := decode.IntsField(m, "froms")
		if err != nil {
			return err
		}
		if len(tiles) != len(froms) {
			return fmt.Errorf("action %d call tiles/froms length mismatch", action.Index)
		}
		for i, tile := range tiles {
			if froms[i] != int64(seatNo) {
				continue
			}
			if !removeTile(&state.hand, tile) {
				return fmt.Errorf("action %d: call uses %s not present in seat %d hand", action.Index, tile, seatNo)
			}
		}
		state.melds++
		state.lastDraw = ""
	case actionAnGangAddGang:
		tile, err := decode.StringField(m, "tiles")
		if err != nil {
			return err
		}
		// Whether this is a closed or added kan is decided by the reconstructed
		// hand content, not by interpreting the unmeasured type value: a closed
		// kan must remove four copies, an added kan exactly one.
		copies := 0
		for _, t := range state.hand {
			if t == tile {
				copies++
			}
		}
		remove := 1
		if copies >= 4 {
			remove = 4
		} else if copies != 1 {
			return fmt.Errorf("action %d: kan of %s finds %d copies in seat %d hand", action.Index, tile, copies, seatNo)
		}
		for i := 0; i < remove; i++ {
			removeTile(&state.hand, tile)
		}
		state.melds++
		if doras, err := decode.StringsField(m, "doras"); err == nil && len(doras) > 0 {
			round.DoraIndicators = doras
		}
	case actionBaBei:
		// Observed only in the schema so far; three-player specifics are
		// unmeasured. Removing the shown north tile would be a guess.
		return fmt.Errorf("action %d: %s reconstruction is unmeasured", action.Index, action.Name)
	}
	return nil
}

func removeTile(hand *[]string, tile string) bool {
	for i, t := range *hand {
		if t == tile {
			*hand = append((*hand)[:i], (*hand)[i+1:]...)
			return true
		}
	}
	return false
}
