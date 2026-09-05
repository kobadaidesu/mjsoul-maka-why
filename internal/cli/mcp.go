package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"

	mjmcp "mjcap/internal/mcp"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// runMCP serves stored games over stdio. The server is read-only and never
// connects to Chrome, the game, or the network.
func runMCP(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("games-dir", filepath.Join("data", "games"), "private directory of stored games produced by mjcap decode --games-dir")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "Usage: mjcap mcp [--games-dir DIR]")
		return 2
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	logger.Info("mcp server starting on stdio", "games_dir", *dir)
	err := mjmcp.New(*dir, "0.1.0").Run(ctx, &sdk.StdioTransport{})
	// A closed stdin means the MCP client disconnected; that is a normal end
	// of session, not a failure.
	if err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) {
		logger.Error("mcp server stopped", "error", err)
		return 1
	}
	return 0
}
