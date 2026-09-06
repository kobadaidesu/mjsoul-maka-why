package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"time"
)

// runIngest chains the existing observe-only capture with offline decode and
// store: attach, let the user open replays + MAKA manually, Ctrl-C, decode.
// It adds no protocol knowledge of its own.
func runIngest(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	endpoint := fs.String("endpoint", "http://127.0.0.1:9222", "existing Chrome debug endpoint")
	meta := fs.String("liqi-meta", "", "exact liqi binding metadata (default: the single file under .cache/mjcap/liqi/)")
	profile := fs.String("protocol", "", "CONFIRMED envelope profile (default: the single file under .cache/mjcap/protocol/)")
	gamesDir := fs.String("games-dir", filepath.Join("data", "games"), "private directory that stores decoded games")
	duration := fs.Duration("duration", 0, "stop capturing after this duration; 0 waits for Ctrl-C")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "Usage: mjcap ingest [--endpoint URL] [--liqi-meta FILE --protocol FILE] [--games-dir DIR] [--duration D]")
		return 2
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	metaPath, err := resolveEvidencePath(*meta, filepath.Join(".cache", "mjcap", "liqi", "*.meta.json"), "--liqi-meta", "run `mjcap fetch-proto --cache-dir .cache/mjcap/liqi` first")
	if err != nil {
		logger.Error("resolve liqi metadata", "error", err)
		return 2
	}
	profilePath, err := resolveEvidencePath(*profile, filepath.Join(".cache", "mjcap", "protocol", "*.json"), "--protocol", "a measured envelope profile is required (docs/phase1-capture.md)")
	if err != nil {
		logger.Error("resolve protocol profile", "error", err)
		return 2
	}
	capturePath := filepath.Join("data", "captures", "capture-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".jsonl")
	logger.Info("ingest: capturing; open the replays and their MAKA views now, then press Ctrl-C", "capture", capturePath)
	captureArgs := []string{"--endpoint", *endpoint, "--out", capturePath, "--liqi-meta", metaPath, "--protocol", profilePath}
	if *duration > 0 {
		captureArgs = append(captureArgs, "--duration", duration.String())
	}
	if code := runCapture(ctx, captureArgs, stderr); code != 0 {
		return code
	}
	logger.Info("ingest: decoding capture", "capture", capturePath)
	return runDecode([]string{"--liqi-meta", metaPath, "--protocol", profilePath, "--games-dir", *gamesDir, capturePath}, stderr)
}

// resolveEvidencePath returns the explicit path, or the unique glob match.
// Zero or multiple matches are explicit errors: evidence files are never
// picked by guesswork.
func resolveEvidencePath(explicit, glob, flagName, hint string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	matches, err := filepath.Glob(glob)
	if err != nil {
		return "", err
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no file matches %s; %s or pass %s", glob, hint, flagName)
	default:
		return "", fmt.Errorf("%d files match %s; pass %s explicitly", len(matches), glob, flagName)
	}
}
