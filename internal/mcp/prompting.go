package mcp

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
