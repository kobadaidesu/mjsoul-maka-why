package mcp

import (
	"context"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverInstructions is handed to every connecting MCP client. It explains
// how to read the data and repeats only measured facts; unverified points
// stay flagged as such (docs/protocol-findings.md).
const serverInstructions = `mjcap serves the user's own Mahjong Soul games, reconstructed from read-only
captures and joined with the official MAKA AI review. Players are seat
numbers 0-3 (0 = the seat that started as East dealer); no names are stored.

Tile codes: 1m-9m (man), 1p-9p (pin), 1s-9s (sou); 0m/0p/0s are the red
fives; honors are 1z=East 2z=South 3z=West 4z=North 5z=Haku 6z=Hatsu 7z=Chun.

MAKA scores are integers 1-99 where higher means more strongly recommended;
score_delta_vs_best is 0 for MAKA's top choice and grows with the gap. The
exact unit is unverified, so treat deltas as recommendation gaps ("over 50 is
a clear mistake" is a reasonable reading), never as points. A decision whose
actual choice is missing from the reported candidates (top 3 only) has an
unknown delta - do not call it 0.

Candidate kinds: discard / riichi_discard (with tile), pass, chi_low,
chi_mid, chi_high (position of the claimed tile in the run), pon, kan, win.
Per-round maka_ratings are raw values whose grade mapping (letters shown by
the client) is not yet measured.

Suggested flow: list_games -> find_mistakes (seat defaults to the stored
self_seat when available) -> get_round for the interesting hands, whose
decisions carry board_before (scores, rivers, melds, riichi state, tiles
left) for concrete explanations. Rivers keep tiles that were later claimed;
claimed tiles are identifiable via melds' froms.`

// analyzePrompt is the reusable analysis request exposed via MCP prompts.
func addAnalyzePrompt(s *sdk.Server) {
	s.AddPrompt(&sdk.Prompt{
		Name:        "analyze_game",
		Title:       "半荘のミス打牌をMAKAの数値付きで解説",
		Description: "Analyze one stored game: rank the player's mistakes with MAKA numbers, use board context, and summarize tendencies.",
		Arguments: []*sdk.PromptArgument{
			{Name: "game_uuid", Description: "Game to analyze; omit to use the newest stored game", Required: false},
			{Name: "seat", Description: "Seat to analyze (0-3); omit to use the stored self_seat", Required: false},
		},
	}, func(_ context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
		target := "the newest stored game (use list_games first)"
		if v := req.Params.Arguments["game_uuid"]; v != "" {
			target = "game " + v
		}
		seat := "the stored self_seat (omit the seat argument)"
		if v := req.Params.Arguments["seat"]; v != "" {
			seat = "seat " + v
		}
		text := fmt.Sprintf(`Analyze %s for %s using the mjcap tools.

1. Call find_mistakes (default threshold) and rank the mistakes by
   score_delta_vs_best. Note how many decisions had no delta (actual choice
   outside MAKA's top 3) instead of treating them as fine.
2. For the worst hands, call get_round and use board_before to explain each
   mistake concretely: what was discarded, what MAKA preferred with its
   score, and the situation (riichi threats, revealed melds, tiles left,
   scores). Mention missed calls or riichi choices when the candidates show
   pass/chi/pon/riichi options with clearly higher scores.
3. Check the per-round maka_ratings for where the player's worst-rated hands
   were, and summarize 2-3 recurring tendencies with concrete examples.
4. Keep the caveat that score deltas are recommendation gaps on MAKA's 1-99
   scale, not point values.

Write the result in Japanese with a compact table of the top mistakes.`, target, seat)
		return &sdk.GetPromptResult{
			Description: "MAKA-grounded mistake review",
			Messages:    []*sdk.PromptMessage{{Role: "user", Content: &sdk.TextContent{Text: text}}},
		}, nil
	})
}
