package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"mjcap/internal/capture"
	"mjcap/internal/decode"
	"mjcap/internal/extract"
)

// Measured 2026-09-05: opening a replay sent this method and its response
// carried the record (docs/protocol-findings.md#phase1-live-capture).
const fetchGameRecordMethod = ".lq.Lobby.fetchGameRecord"

func runDecode(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("decode", flag.ContinueOnError)
	fs.SetOutput(stderr)
	uuid := fs.String("game-uuid", "", "only decode the record with this game uuid")
	out := fs.String("out", "", "write reconstruction JSON to this new file (default: stdout)")
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
	var games []extract.Game
	replayErr := capture.Replay(f, func(e capture.Event) error {
		if err := checkCaptureBinding(e, n, meta); err != nil {
			return err
		}
		frame := d.Observe(e)
		if frame.Kind != "response" || frame.Name != fetchGameRecordMethod || frame.Message == nil {
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
		issues := len(game.Issues)
		decisions := 0
		for _, r := range game.Rounds {
			issues += len(r.Issues)
			decisions += len(r.Decisions)
		}
		logger.Info("game record reconstructed", "seq", e.Seq, "rounds", len(game.Rounds), "decisions", decisions, "issues", issues, "record_version", game.Version)
		games = append(games, game)
		return nil
	})
	closeErr := f.Close()
	if replayErr != nil || closeErr != nil {
		logger.Error("decode capture", "error", errors.Join(replayErr, closeErr))
		return 1
	}
	if len(games) == 0 {
		logger.Error("no matching game record response in capture")
		return 1
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
