package extract

import (
	"mjcap/internal/decode"
)

// Board is the table state right before a discard decision, reconstructed
// from measured record events (docs/protocol-findings.md#phase2-record-decode
// and the LiQiSuccess measurements referenced from #board-state).
//
// Rivers keep every discarded tile in order, including tiles later claimed by
// calls; claimed tiles are visible through the melds' froms entries instead
// of being removed, so "which tiles passed" stays complete.
type Board struct {
	Scores         []int64    `json:"scores"`
	DoraIndicators []string   `json:"dora_indicators"`
	TilesLeft      uint64     `json:"tiles_left"`
	RiichiSticks   uint64     `json:"riichi_sticks"`
	RiichiDeclared []bool     `json:"riichi_declared"`
	Rivers         [][]string `json:"rivers"`
	Melds          [][]Meld   `json:"melds"`
}

// Meld is one call. Froms parallels Tiles; entries with the caller's own seat
// came from hand. A kan recorded via RecordAnGangAddGang carries Kan=true and
// the removed copies (four for a closed kan, one for an added kan).
type Meld struct {
	Tiles []string `json:"tiles"`
	Froms []int64  `json:"froms,omitempty"`
	Kan   bool     `json:"kan,omitempty"`
}

// boardState tracks the live table while replaying one round.
type boardState struct {
	scores    []int64
	doras     []string
	tilesLeft uint64
	sticks    uint64
	riichi    []bool
	rivers    [][]string
	melds     [][]Meld
}

func newBoardState(scores []int64, doras []string, tilesLeft, sticks uint64) *boardState {
	n := len(scores)
	return &boardState{
		scores:    append([]int64(nil), scores...),
		doras:     append([]string(nil), doras...),
		tilesLeft: tilesLeft,
		sticks:    sticks,
		riichi:    make([]bool, n),
		rivers:    make([][]string, n),
		melds:     make([][]Meld, n),
	}
}

// snapshot deep-copies the state for one decision.
func (b *boardState) snapshot() *Board {
	rivers := make([][]string, len(b.rivers))
	for i, r := range b.rivers {
		rivers[i] = append([]string{}, r...)
	}
	melds := make([][]Meld, len(b.melds))
	for i, m := range b.melds {
		melds[i] = append([]Meld{}, m...)
	}
	return &Board{
		Scores:         append([]int64(nil), b.scores...),
		DoraIndicators: append([]string(nil), b.doras...),
		TilesLeft:      b.tilesLeft,
		RiichiSticks:   b.sticks,
		RiichiDeclared: append([]bool(nil), b.riichi...),
		Rivers:         rivers,
		Melds:          melds,
	}
}

// noteLiqiSuccess reads DealTile.liqi when explicitly present: score is the
// declarer's balance after the 1000-point stick and liqibang the resulting
// stick count (verified against every riichi of the captured game).
func (b *boardState) noteLiqiSuccess(action decode.ActionRecord) {
	if !decode.HasField(action.Message, "liqi") {
		return
	}
	liqi, err := decode.MessageField(action.Message, "liqi")
	if err != nil {
		return
	}
	seat, err1 := decode.UintField(liqi, "seat")
	score, err2 := decode.IntField(liqi, "score")
	sticks, err3 := decode.UintField(liqi, "liqibang")
	if err1 != nil || err2 != nil || err3 != nil || int(seat) >= len(b.scores) {
		return
	}
	b.scores[seat] = score
	b.sticks = sticks
}
