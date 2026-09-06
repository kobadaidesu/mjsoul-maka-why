package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mjcap/internal/extract"
	"mjcap/internal/store"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func delta(v int64) *int64 { return &v }

func fixtureGame() extract.Game {
	return extract.Game{
		UUID: "synthetic-uuid-mcp", Version: 1, Seats: 4, MakaUUID: "synthetic-uuid-mcp",
		Mode: &extract.GameMode{Category: 1, ModeID: 9},
		Rounds: []extract.Round{
			{
				HandIndex: 0, KyokuIndex: 0, Wind: "east", Kyoku: 1, Honba: 0, Label: "東1局0本場",
				DealerSeat: 0, ScoresStart: []int64{25000, 25000, 25000, 25000},
				EndStatus: "hule", EndScores: []int64{26000, 25000, 24000, 25000},
				MakaRatings: []extract.SeerRating{{Seat: 0, Rating: 55}, {Seat: 2, Rating: 16}},
				Decisions: []extract.Decision{
					{Seat: 2, TurnIndex: 1, ActionIndex: 3, HandBefore: []string{"1m", "2m"}, Discard: "1m",
						Maka: &extract.MakaEval{Candidates: []extract.SeerCandidate{{Action: 111, Score: 60, Tile: "1m"}}, BestScore: 60, ActualScore: delta(60), ScoreDeltaVsBest: delta(0)}},
					{Seat: 2, TurnIndex: 2, ActionIndex: 7, HandBefore: []string{"1m", "3p"}, Draw: "3p", Discard: "3p",
						Maka: &extract.MakaEval{Candidates: []extract.SeerCandidate{{Action: 111, Score: 80, Tile: "1m"}, {Action: 123, Score: 15, Tile: "3p"}}, BestScore: 80, ActualScore: delta(15), ScoreDeltaVsBest: delta(65)}},
					{Seat: 2, TurnIndex: 3, ActionIndex: 9, HandBefore: []string{"9s"}, Discard: "9s",
						Maka: &extract.MakaEval{Candidates: []extract.SeerCandidate{{Action: 141, Score: 90, Tile: "1z"}}, BestScore: 90}},
					{Seat: 1, TurnIndex: 1, ActionIndex: 5, HandBefore: []string{"5z"}, Discard: "5z",
						Maka: &extract.MakaEval{Candidates: []extract.SeerCandidate{{Action: 145, Score: 50, Tile: "5z"}}, BestScore: 50, ActualScore: delta(30), ScoreDeltaVsBest: delta(20)}},
				},
			},
		},
	}
}

func session(t *testing.T, dir string) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	server := New(dir, "test")
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *sdk.ClientSession, tool string, args map[string]any, out any) (errText string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	if res.IsError {
		for _, c := range res.Content {
			if tc, ok := c.(*sdk.TextContent); ok {
				errText += tc.Text
			}
		}
		if errText == "" {
			t.Fatalf("%s failed without message", tool)
		}
		return errText
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s output: %v (%s)", tool, err, raw)
	}
	return ""
}

func TestHTTPHandlerRequiresToken(t *testing.T) {
	dir := t.TempDir()
	if _, err := store.Save(dir, store.File{CapturedAt: time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC), Game: fixtureGame()}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(Handler(dir, "test", "secret-token-secret-token-secret-token"))
	defer ts.Close()

	for _, path := range []string{"/mcp", "/wrong-token/mcp", "/"} {
		resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("path %s: status %d", path, resp.StatusCode)
		}
	}

	ctx := context.Background()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: ts.URL + "/secret-token-secret-token-secret-token/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "list_games", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("http list_games: %v %+v", err, res)
	}
	var list ListGamesOutput
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &list); err != nil || len(list.Games) != 1 || list.Games[0].GameUUID != "synthetic-uuid-mcp" {
		t.Fatalf("http list_games output: %v %+v", err, list)
	}
}

func TestMCPToolsServeStoredGames(t *testing.T) {
	dir := t.TempDir()
	if _, err := store.Save(dir, store.File{CapturedAt: time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC), Game: fixtureGame()}); err != nil {
		t.Fatal(err)
	}
	cs := session(t, dir)

	var list ListGamesOutput
	if msg := call(t, cs, "list_games", map[string]any{}, &list); msg != "" {
		t.Fatal(msg)
	}
	if len(list.Games) != 1 || list.Games[0].GameUUID != "synthetic-uuid-mcp" || !list.Games[0].MakaJoined || list.Games[0].FinalScores[0] != 26000 {
		t.Fatalf("list_games %+v", list)
	}

	var round RoundOutput
	if msg := call(t, cs, "get_round", map[string]any{"game_uuid": "synthetic-uuid-mcp", "hand_index": 0}, &round); msg != "" {
		t.Fatal(msg)
	}
	if round.Round.Label != "東1局0本場" || len(round.Round.Decisions) != 4 || round.Round.MakaRatings[1].Rating != 16 {
		t.Fatalf("get_round %+v", round)
	}
	if msg := call(t, cs, "get_round", map[string]any{"game_uuid": "synthetic-uuid-mcp", "hand_index": 9}, &round); !strings.Contains(msg, "hand_index") {
		t.Fatalf("missing round not rejected: %q", msg)
	}

	var mistakes FindMistakesOutput
	if msg := call(t, cs, "find_mistakes", map[string]any{"game_uuid": "synthetic-uuid-mcp", "seat": 2}, &mistakes); msg != "" {
		t.Fatal(msg)
	}
	if len(mistakes.Mistakes) != 1 || mistakes.Mistakes[0].ScoreDeltaVsBest != 65 || mistakes.Mistakes[0].Discard != "3p" {
		t.Fatalf("find_mistakes default threshold %+v", mistakes)
	}
	if mistakes.UnknownDeltaDecisions != 1 || mistakes.Thresh != 10 {
		t.Fatalf("find_mistakes accounting %+v", mistakes)
	}
	if msg := call(t, cs, "find_mistakes", map[string]any{"game_uuid": "synthetic-uuid-mcp", "seat": 2, "threshold": 0}, &mistakes); msg != "" {
		t.Fatal(msg)
	}
	if len(mistakes.Mistakes) != 2 || mistakes.Mistakes[0].ScoreDeltaVsBest != 65 || mistakes.Mistakes[1].ScoreDeltaVsBest != 0 {
		t.Fatalf("find_mistakes threshold 0 %+v", mistakes)
	}

	// The user's seat is not stored: omitting seat must be an explicit error.
	msg := call(t, cs, "find_mistakes", map[string]any{"game_uuid": "synthetic-uuid-mcp"}, &mistakes)
	if !strings.Contains(msg, "seat is required") {
		t.Fatalf("seatless call not rejected: %q", msg)
	}
	if msg := call(t, cs, "find_mistakes", map[string]any{"game_uuid": "synthetic-uuid-mcp", "seat": 9}, &mistakes); !strings.Contains(msg, "outside") {
		t.Fatalf("bad seat not rejected: %q", msg)
	}
	if msg := call(t, cs, "find_mistakes", map[string]any{"game_uuid": "no-such-game-uuid", "seat": 2}, &mistakes); msg == "" {
		t.Fatal("unknown game accepted")
	}
}
