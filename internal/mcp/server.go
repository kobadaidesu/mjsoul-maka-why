// Package mcp exposes stored game reconstructions to MCP clients. The server
// is read-only: it serves files from the games directory and has no access to
// Chrome, the game, or the network.
package mcp

import (
	"context"
	"fmt"
	"sort"
	"time"

	"mjcap/internal/extract"
	"mjcap/internal/store"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// New builds the MCP server with the three read-only tools.
func New(gamesDir, version string) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "mjcap", Version: version}, nil)
	h := handlers{dir: gamesDir}
	sdk.AddTool(s, &sdk.Tool{
		Name:        "list_games",
		Description: "List stored Mahjong Soul game reconstructions with MAKA joins, newest first. Players are identified by seat number only.",
	}, h.listGames)
	sdk.AddTool(s, &sdk.Tool{
		Name:        "get_round",
		Description: "Return one hand (round) of a stored game: label, dealer, scores, every discard decision with the hand before it, and joined MAKA candidates/ratings.",
	}, h.getRound)
	sdk.AddTool(s, &sdk.Tool{
		Name:        "find_mistakes",
		Description: "Return discard decisions whose MAKA score_delta_vs_best is at least the threshold (default 10; 0 = best choice). The stored data does not identify which seat is the user, so seat is required.",
	}, h.findMistakes)
	return s
}

type handlers struct{ dir string }

type ListGamesInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"maximum number of games to return; default 20"`
}

type GameSummary struct {
	GameUUID    string            `json:"game_uuid"`
	CapturedAt  time.Time         `json:"captured_at"`
	Seats       int               `json:"seats"`
	Rounds      int               `json:"rounds"`
	Mode        *extract.GameMode `json:"mode,omitempty"`
	FinalScores []int64           `json:"final_scores,omitempty"`
	MakaJoined  bool              `json:"maka_joined"`
	Issues      int               `json:"issues"`
	Note        string            `json:"note,omitempty"`
}

func (h handlers) listGames(_ context.Context, _ *sdk.CallToolRequest, in ListGamesInput) (*sdk.CallToolResult, []GameSummary, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	files, err := store.List(h.dir)
	if err != nil {
		return nil, nil, err
	}
	out := make([]GameSummary, 0, len(files))
	for _, f := range files {
		if len(out) >= limit {
			break
		}
		issues := len(f.Game.Issues)
		var finals []int64
		for _, r := range f.Game.Rounds {
			issues += len(r.Issues)
			if len(r.EndScores) > 0 {
				finals = r.EndScores
			}
		}
		out = append(out, GameSummary{
			GameUUID:    f.Game.UUID,
			CapturedAt:  f.CapturedAt,
			Seats:       f.Game.Seats,
			Rounds:      len(f.Game.Rounds),
			Mode:        f.Game.Mode,
			FinalScores: finals,
			MakaJoined:  f.Game.MakaUUID != "",
			Issues:      issues,
			Note:        "seat identity of the user is not stored; the overall MAKA rank shown by the client has no measured source yet",
		})
	}
	return nil, out, nil
}

type GetRoundInput struct {
	GameUUID  string `json:"game_uuid"`
	HandIndex int    `json:"hand_index" jsonschema:"sequential hand number including dealer repeats, starting at 0"`
}

type RoundOutput struct {
	GameUUID string        `json:"game_uuid"`
	Round    extract.Round `json:"round"`
}

func (h handlers) getRound(_ context.Context, _ *sdk.CallToolRequest, in GetRoundInput) (*sdk.CallToolResult, RoundOutput, error) {
	f, err := store.LoadByUUID(h.dir, in.GameUUID)
	if err != nil {
		return nil, RoundOutput{}, err
	}
	for _, r := range f.Game.Rounds {
		if r.HandIndex == in.HandIndex {
			return nil, RoundOutput{GameUUID: f.Game.UUID, Round: r}, nil
		}
	}
	return nil, RoundOutput{}, fmt.Errorf("game %s has no hand_index %d (0..%d)", in.GameUUID, in.HandIndex, len(f.Game.Rounds)-1)
}

type FindMistakesInput struct {
	GameUUID  string `json:"game_uuid"`
	Threshold *int64 `json:"threshold,omitempty" jsonschema:"minimum score_delta_vs_best to report; default 10"`
	Seat      *int   `json:"seat,omitempty" jsonschema:"seat to inspect (0-3). Required: the stored data does not record which seat is the user"`
}

type Mistake struct {
	Label            string                  `json:"label"`
	HandIndex        int                     `json:"hand_index"`
	TurnIndex        int                     `json:"turn_index"`
	ActionIndex      int                     `json:"action_index"`
	Seat             int                     `json:"seat"`
	HandBefore       []string                `json:"hand_before"`
	Draw             string                  `json:"draw,omitempty"`
	Discard          string                  `json:"discard"`
	Riichi           bool                    `json:"riichi"`
	Tsumogiri        bool                    `json:"tsumogiri"`
	Candidates       []extract.SeerCandidate `json:"candidates"`
	BestScore        int64                   `json:"best_score"`
	ActualScore      int64                   `json:"actual_score"`
	ScoreDeltaVsBest int64                   `json:"score_delta_vs_best"`
}

type FindMistakesOutput struct {
	GameUUID string    `json:"game_uuid"`
	Seat     int       `json:"seat"`
	Thresh   int64     `json:"threshold"`
	Mistakes []Mistake `json:"mistakes"`
	// Decisions where the actual discard was not among MAKA's reported
	// candidates: their delta is unknown, not zero, so they are counted but
	// never ranked.
	UnknownDeltaDecisions int    `json:"unknown_delta_decisions"`
	Note                  string `json:"note,omitempty"`
}

func (h handlers) findMistakes(_ context.Context, _ *sdk.CallToolRequest, in FindMistakesInput) (*sdk.CallToolResult, FindMistakesOutput, error) {
	if in.Seat == nil {
		return nil, FindMistakesOutput{}, fmt.Errorf("seat is required: the stored reconstruction does not identify which seat is the user, and guessing seat 0 would report someone else's play")
	}
	threshold := int64(10)
	if in.Threshold != nil {
		threshold = *in.Threshold
	}
	f, err := store.LoadByUUID(h.dir, in.GameUUID)
	if err != nil {
		return nil, FindMistakesOutput{}, err
	}
	if *in.Seat < 0 || *in.Seat >= f.Game.Seats {
		return nil, FindMistakesOutput{}, fmt.Errorf("seat %d outside 0..%d", *in.Seat, f.Game.Seats-1)
	}
	out := FindMistakesOutput{GameUUID: f.Game.UUID, Seat: *in.Seat, Thresh: threshold,
		Note: "score_delta_vs_best is the gap in MAKA's recommendation score (1-99 scale, 0 = MAKA's top choice); its exact unit is unverified"}
	for _, r := range f.Game.Rounds {
		for _, d := range r.Decisions {
			if d.Seat != *in.Seat || d.Maka == nil {
				continue
			}
			if d.Maka.ScoreDeltaVsBest == nil {
				out.UnknownDeltaDecisions++
				continue
			}
			if *d.Maka.ScoreDeltaVsBest < threshold {
				continue
			}
			out.Mistakes = append(out.Mistakes, Mistake{
				Label: r.Label, HandIndex: r.HandIndex, TurnIndex: d.TurnIndex, ActionIndex: d.ActionIndex,
				Seat: d.Seat, HandBefore: d.HandBefore, Draw: d.Draw, Discard: d.Discard,
				Riichi: d.Riichi, Tsumogiri: d.Tsumogiri, Candidates: d.Maka.Candidates,
				BestScore: d.Maka.BestScore, ActualScore: *d.Maka.ActualScore, ScoreDeltaVsBest: *d.Maka.ScoreDeltaVsBest,
			})
		}
	}
	sort.SliceStable(out.Mistakes, func(i, j int) bool { return out.Mistakes[i].ScoreDeltaVsBest > out.Mistakes[j].ScoreDeltaVsBest })
	return nil, out, nil
}
