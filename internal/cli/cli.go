package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"mjcap/internal/liqi"
)

// Run owns CLI parsing/logging; main only wires process lifetime and streams.
func Run(ctx context.Context, args []string, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stderr, "Usage: mjcap <fetch-proto|capture|inspect|decode|ingest|mcp> [flags]\nObserve existing Chrome traffic, decode captured game records, or serve stored games over MCP stdio.")
		return 0
	}
	if args[0] == "mcp" {
		return runMCP(ctx, args[1:], stderr)
	}
	if args[0] == "ingest" {
		return runIngest(ctx, args[1:], stderr)
	}
	if args[0] == "capture" {
		return runCapture(ctx, args[1:], stderr)
	}
	if args[0] == "inspect" {
		return runInspect(args[1:], stderr)
	}
	if args[0] == "decode" {
		return runDecode(args[1:], stderr)
	}
	if args[0] != "fetch-proto" {
		fmt.Fprintln(stderr, "Unknown subcommand; available: fetch-proto, capture, inspect, decode, ingest, mcp.")
		return 2
	}
	fs := flag.NewFlagSet("fetch-proto", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cache := fs.String("cache-dir", "", "schema cache directory (default: OS cache/mjcap/liqi)")
	expected := fs.String("sha256", "", "require this exact schema SHA-256")
	timeout := fs.Duration("timeout", 90*time.Second, "total fetch timeout")
	debug := fs.Bool("debug", false, "enable debug logging")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || *timeout <= 0 {
		fmt.Fprintln(stderr, "fetch-proto accepts no positional arguments and requires a positive timeout")
		return 2
	}
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
	if *cache == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			logger.Error("resolve cache directory", "error", err)
			return 1
		}
		*cache = filepath.Join(base, "mjcap", "liqi")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	r, err := liqi.NewResolver().Fetch(ctx, *cache, *expected)
	if err != nil {
		logger.Error("fetch-proto failed", "error", err)
		return 1
	}
	if r.Cached {
		logger.Warn("using exact-version cache", "reason", r.FallbackReason)
	}
	logger.Info("resolved liqi", "game_version", r.Metadata.GameVersion, "liqi_resource_version", r.Metadata.ResourceVersion,
		"sha256", r.Metadata.SHA256, "source_url", r.Metadata.SourceURL, "cached", r.Cached,
		"descriptor_files", r.Registry.Counts.Files, "message_descriptors", r.Registry.Counts.Messages, "enum_descriptors", r.Registry.Counts.Enums)
	// These are required/expected candidates specified by the constitution.
	// Presence in this registry confirms only schema existence, not live use.
	if _, err := r.Registry.Message(".lq.Wrapper"); err != nil {
		logger.Error("required candidate missing", "message_name", ".lq.Wrapper", "error", err)
		return 1
	}
	logger.Info("required candidate found", "message_name", ".lq.Wrapper")
	if _, err := r.Registry.Message(".lq.ResGameRecord"); err != nil {
		logger.Warn("expected candidate missing", "message_name", ".lq.ResGameRecord", "error", err)
	} else {
		logger.Info("expected candidate found", "message_name", ".lq.ResGameRecord")
	}
	logger.Warn("active browser schema match is unverified; Phase 1 capture must confirm it")
	return 0
}
