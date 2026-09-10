package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"mjcap/internal/capture"
	"mjcap/internal/decode"
	"mjcap/internal/extract"
	"mjcap/internal/liqi"
	"mjcap/internal/store"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// gameMode copies the raw mode identifiers from RecordGame.config without
// interpreting them; missing pieces simply stay unset.
func gameMode(head protoreflect.Message) *extract.GameMode {
	config, err := decode.MessageField(head, "config")
	if err != nil {
		return nil
	}
	mode := &extract.GameMode{}
	if v, err := decode.UintField(config, "category"); err == nil {
		mode.Category = v
	}
	if m, err := decode.MessageField(config, "mode"); err == nil {
		if v, err := decode.UintField(m, "mode"); err == nil {
			mode.Mode = v
		}
	}
	if meta, err := decode.MessageField(config, "meta"); err == nil {
		if v, err := decode.UintField(meta, "mode_id"); err == nil {
			mode.ModeID = v
		}
	}
	return mode
}

// Measured 2026-09-05: opening a replay sent fetchGameRecord, showing MAKA
// sent fetchSeerReport, and the login sequence answered oauth2Login with
// ResLogin carrying the user's account_id
// (docs/protocol-findings.md#phase1-live-capture, #phase1-reload-capture).
const (
	fetchGameRecordMethod = ".lq.Lobby.fetchGameRecord"
	fetchSeerReportMethod = ".lq.Lobby.fetchSeerReport"
	oauth2LoginMethod     = ".lq.Lobby.oauth2Login"
)

// selfSeatFromHead matches known logged-in accounts against the record's
// seat table and returns only the seat number, and only when exactly one
// seat matches — several known accounts in the same game (e.g. a shared
// captures directory) are ambiguous and stay unknown. The account
// identifiers stay in memory; they are never stored or logged (AGENTS.md
// privacy rules).
func selfSeatFromHead(head protoreflect.Message, loginIDs map[uint64]bool) *int {
	if len(loginIDs) == 0 {
		return nil
	}
	accounts, err := decode.MessagesField(head, "accounts")
	if err != nil {
		return nil
	}
	var seat *int
	for _, acc := range accounts {
		id, err := decode.UintField(acc, "account_id")
		if err != nil || !loginIDs[id] {
			continue
		}
		s, err := decode.UintField(acc, "seat")
		if err != nil || seat != nil {
			return nil
		}
		v := int(s)
		seat = &v
	}
	return seat
}

func runDecode(args []string, stderr io.Writer) int {
	return runDecodeWith(args, stderr, nil)
}

// decodeOptions is the parsed CLI surface of the decode subcommand.
type decodeOptions struct {
	uuid, out, gamesDir, loginDir, capturePath string
	names                                      nameOptions
}

// parseDecodeFlags returns nil with the exit code when parsing stops
// (--help or invalid arguments).
func parseDecodeFlags(args []string, stderr io.Writer) (*decodeOptions, int) {
	fs := flag.NewFlagSet("decode", flag.ContinueOnError)
	fs.SetOutput(stderr)
	o := &decodeOptions{}
	fs.StringVar(&o.uuid, "game-uuid", "", "only decode the record with this game uuid")
	fs.StringVar(&o.out, "out", "", "write reconstruction JSON to this new file (default: stdout)")
	fs.StringVar(&o.gamesDir, "games-dir", "", "also store each game as {uuid}.json (schema_version 1) in this private directory")
	fs.StringVar(&o.loginDir, "login-from-captures", "", "when this capture has no login, resolve the self seat from the newest login response in this captures directory (ids stay in memory, never stored or logged)")
	o.names.flags(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, 0
		}
		return nil, 2
	}
	if fs.NArg() != 1 || o.names.meta == "" || o.names.profile == "" {
		fmt.Fprintln(stderr, "Usage: mjcap decode --liqi-meta FILE --protocol FILE [--game-uuid UUID] [--out FILE] CAPTURE.jsonl")
		return nil, 2
	}
	o.capturePath = fs.Arg(0)
	return o, 0
}

// pendingGame keeps one reconstructed record with everything the later
// join/store pass needs.
type pendingGame struct {
	game       extract.Game
	detail     decode.GameDetail
	head       protoreflect.Message
	seq        uint64
	capturedAt time.Time
}

// collectCaptureGames replays one capture stream and gathers reconstructed
// games in arrival order, MAKA reports keyed by game uuid (latest wins), and
// login ids. The ids live in memory only; callers must never store or log
// them. Opening and closing the capture stays with the caller so its error
// reporting keeps the original boundaries.
func collectCaptureGames(f io.Reader, d *decode.Decoder, n *decode.Names, meta *liqi.Metadata, uuidFilter string) ([]pendingGame, map[string]protoreflect.Message, map[uint64]bool, error) {
	var pending []pendingGame
	reports := map[string]protoreflect.Message{}
	loginIDs := map[uint64]bool{}
	replayErr := capture.Replay(f, func(e capture.Event) error {
		if err := checkCaptureBinding(e, n, meta); err != nil {
			return err
		}
		frame := d.Observe(e)
		if frame.Kind != "response" || frame.Message == nil {
			return nil
		}
		switch frame.Name {
		case oauth2LoginMethod:
			if id, err := decode.UintField(frame.Message, "account_id"); err == nil && id != 0 {
				loginIDs[id] = true
			}
			return nil
		case fetchSeerReportMethod:
			report, err := decode.MessageField(frame.Message, "report")
			if err != nil {
				return fmt.Errorf("seq %d: %w", e.Seq, err)
			}
			reportUUID, err := decode.StringField(report, "uuid")
			if err != nil {
				return fmt.Errorf("seq %d: %w", e.Seq, err)
			}
			if reportUUID != "" {
				reports[reportUUID] = report
			}
			return nil
		case fetchGameRecordMethod:
		default:
			return nil
		}
		head, err := decode.MessageField(frame.Message, "head")
		if err != nil {
			return fmt.Errorf("seq %d: %w", e.Seq, err)
		}
		gameUUID, err := decode.StringField(head, "uuid")
		if err != nil {
			return fmt.Errorf("seq %d: %w", e.Seq, err)
		}
		if uuidFilter != "" && gameUUID != uuidFilter {
			return nil
		}
		detail, err := d.GameDetailActions(frame.Message)
		if err != nil {
			return fmt.Errorf("seq %d: %w", e.Seq, err)
		}
		game, err := extract.Rounds(gameUUID, detail)
		if err != nil {
			return fmt.Errorf("seq %d: %w", e.Seq, err)
		}
		game.Mode = gameMode(head)
		if v, err := decode.UintField(head, "start_time"); err == nil {
			game.StartTime = v
		}
		if v, err := decode.UintField(head, "end_time"); err == nil {
			game.EndTime = v
		}
		pending = append(pending, pendingGame{game: game, detail: detail, head: head, seq: e.Seq, capturedAt: e.CapturedAt})
		return nil
	})
	if replayErr != nil {
		return nil, nil, nil, replayErr
	}
	return pending, reports, loginIDs, nil
}

// joinAndStoreGames finalizes each game in capture order: self seat, MAKA
// join, log, then store immediately. A failure on a later game keeps the
// earlier stores in place — partial progress is intentional and must not be
// replaced by a join-everything-then-store pass.
func joinAndStoreGames(logger *slog.Logger, pending []pendingGame, reports map[string]protoreflect.Message, loginIDs map[uint64]bool, gamesDir string) ([]extract.Game, int) {
	var games []extract.Game
	for _, p := range pending {
		p.game.SelfSeat = selfSeatFromHead(p.head, loginIDs)
		if report, ok := reports[p.game.UUID]; ok {
			if err := extract.JoinSeer(&p.game, p.detail, report); err != nil {
				logger.Error("join seer report", "seq", p.seq, "error", err)
				return nil, 1
			}
		}
		issues, decisions, joined := len(p.game.Issues), 0, 0
		for _, r := range p.game.Rounds {
			issues += len(r.Issues)
			decisions += len(r.Decisions)
			for _, d := range r.Decisions {
				if d.Maka != nil {
					joined++
				}
			}
		}
		logger.Info("game record reconstructed", cliEventKey, cliEventGameReconstructed, "seq", p.seq, "rounds", len(p.game.Rounds), "decisions", decisions, "maka_joined_decisions", joined, "maka_report", p.game.MakaUUID != "", "self_seat_known", p.game.SelfSeat != nil, "issues", issues, "record_version", p.game.Version)
		if gamesDir != "" {
			path, err := store.Save(gamesDir, store.File{CapturedAt: p.capturedAt, Game: p.game})
			if err != nil {
				logger.Error("store game", "error", err)
				return nil, 1
			}
			logger.Info("game stored", cliEventKey, cliEventGameStored, "path", path)
		}
		games = append(games, p.game)
	}
	return games, 0
}

// writeGamesOutput emits the reconstruction JSON to stdout or --out (0600,
// created exclusively, never overwriting). With --games-dir and no --out the
// stores are the only output.
func writeGamesOutput(logger *slog.Logger, games []extract.Game, out, gamesDir string) int {
	if out == "" && gamesDir != "" {
		return 0
	}
	encoded, err := json.MarshalIndent(games, "", "  ")
	if err != nil {
		logger.Error("encode reconstruction", "error", err)
		return 1
	}
	encoded = append(encoded, '\n')
	if out == "" {
		if _, err := os.Stdout.Write(encoded); err != nil {
			logger.Error("write reconstruction", "error", err)
			return 1
		}
		return 0
	}
	w, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		logger.Error("create reconstruction file without overwriting", "error", err)
		return 1
	}
	_, writeErr := w.Write(encoded)
	if err := errors.Join(writeErr, w.Close()); err != nil {
		logger.Error("write reconstruction", "error", err)
		return 1
	}
	logger.Info("reconstruction saved", "path", out, "games", len(games))
	return 0
}

// runDecodeWith lets ingest inject its own logger (the friendly progress
// view); logger == nil keeps the plain text logs.
func runDecodeWith(args []string, stderr io.Writer, logger *slog.Logger) int {
	opts, code := parseDecodeFlags(args, stderr)
	if opts == nil {
		return code
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(stderr, nil))
	}
	n, meta, err := opts.names.load(logger)
	if err != nil || n == nil {
		logger.Error("load decode evidence", "error", err)
		return 2
	}
	d, err := decode.NewDecoder(n.Evidence(), opts.names.resource)
	if err != nil {
		logger.Error("build decoder", "error", err)
		return 2
	}
	f, err := os.Open(opts.capturePath)
	if err != nil {
		logger.Error("open capture", "error", err)
		return 1
	}
	pending, reports, loginIDs, replayErr := collectCaptureGames(f, d, n, meta, opts.uuid)
	closeErr := f.Close()
	if replayErr != nil || closeErr != nil {
		logger.Error("decode capture", "error", errors.Join(replayErr, closeErr))
		return 1
	}
	if len(pending) == 0 {
		logger.Error("no matching game record response in capture", cliEventKey, cliEventNoGameRecord)
		return 1
	}
	if len(loginIDs) == 0 && opts.loginDir != "" {
		ids, source := reuseLoginIDs(opts.loginDir, opts.capturePath, func(path string) []uint64 {
			return loginIDsFromCapture(path, n, meta, opts.names.resource)
		})
		for _, id := range ids {
			loginIDs[id] = true
		}
		if len(ids) > 0 {
			logger.Info("self seat login reused from earlier capture", cliEventKey, cliEventLoginReused, "capture", filepath.Base(source))
		}
	}
	games, code := joinAndStoreGames(logger, pending, reports, loginIDs, opts.gamesDir)
	if code != 0 {
		return code
	}
	return writeGamesOutput(logger, games, opts.out, opts.gamesDir)
}
