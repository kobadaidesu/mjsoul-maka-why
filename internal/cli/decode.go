package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"mjcap/internal/capture"
	"mjcap/internal/decode"
	"mjcap/internal/extract"
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

// Measured 2026-09-05: opening a replay sent fetchGameRecord and showing MAKA
// sent fetchSeerReport (docs/protocol-findings.md#phase1-live-capture).
const (
	fetchGameRecordMethod = ".lq.Lobby.fetchGameRecord"
	fetchSeerReportMethod = ".lq.Lobby.fetchSeerReport"
)

func runDecode(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("decode", flag.ContinueOnError)
	fs.SetOutput(stderr)
	uuid := fs.String("game-uuid", "", "only decode the record with this game uuid")
	out := fs.String("out", "", "write reconstruction JSON to this new file (default: stdout)")
	gamesDir := fs.String("games-dir", "", "also store each game as {uuid}.json (schema_version 1) in this private directory")
	var names nameOptions
	names.flags(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 || names.meta == "" || names.profile == "" {
		fmt.Fprintln(stderr, "Usage: mjcap decode --liqi-meta FILE --protocol FILE [--game-uuid UUID] [--out FILE] CAPTURE.jsonl")
		return 2
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	n, meta, err := names.load(logger)
	if err != nil || n == nil {
		logger.Error("load decode evidence", "error", err)
		return 2
	}
	d, err := decode.NewDecoder(n.Evidence(), names.resource)
	if err != nil {
		logger.Error("build decoder", "error", err)
		return 2
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		logger.Error("open capture", "error", err)
		return 1
	}
	type pendingGame struct {
		game       extract.Game
		detail     decode.GameDetail
		seq        uint64
		capturedAt time.Time
	}
	var pending []pendingGame
	reports := map[string]protoreflect.Message{}
	replayErr := capture.Replay(f, func(e capture.Event) error {
		if err := checkCaptureBinding(e, n, meta); err != nil {
			return err
		}
		frame := d.Observe(e)
		if frame.Kind != "response" || frame.Message == nil {
			return nil
		}
		switch frame.Name {
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
		if *uuid != "" && gameUUID != *uuid {
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
		pending = append(pending, pendingGame{game: game, detail: detail, seq: e.Seq, capturedAt: e.CapturedAt})
		return nil
	})
	closeErr := f.Close()
	if replayErr != nil || closeErr != nil {
		logger.Error("decode capture", "error", errors.Join(replayErr, closeErr))
		return 1
	}
	if len(pending) == 0 {
		logger.Error("no matching game record response in capture")
		return 1
	}
	var games []extract.Game
	for _, p := range pending {
		if report, ok := reports[p.game.UUID]; ok {
			if err := extract.JoinSeer(&p.game, p.detail, report); err != nil {
				logger.Error("join seer report", "seq", p.seq, "error", err)
				return 1
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
		logger.Info("game record reconstructed", "seq", p.seq, "rounds", len(p.game.Rounds), "decisions", decisions, "maka_joined_decisions", joined, "maka_report", p.game.MakaUUID != "", "issues", issues, "record_version", p.game.Version)
		if *gamesDir != "" {
			path, err := store.Save(*gamesDir, store.File{CapturedAt: p.capturedAt, Game: p.game})
			if err != nil {
				logger.Error("store game", "error", err)
				return 1
			}
			logger.Info("game stored", "path", path)
		}
		games = append(games, p.game)
	}
	if *out == "" && *gamesDir != "" {
		return 0
	}
	encoded, err := json.MarshalIndent(games, "", "  ")
	if err != nil {
		logger.Error("encode reconstruction", "error", err)
		return 1
	}
	encoded = append(encoded, '\n')
	if *out == "" {
		if _, err := os.Stdout.Write(encoded); err != nil {
			logger.Error("write reconstruction", "error", err)
			return 1
		}
		return 0
	}
	w, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		logger.Error("create reconstruction file without overwriting", "error", err)
		return 1
	}
	_, writeErr := w.Write(encoded)
	if err := errors.Join(writeErr, w.Close()); err != nil {
		logger.Error("write reconstruction", "error", err)
		return 1
	}
	logger.Info("reconstruction saved", "path", *out, "games", len(games))
	return 0
}
