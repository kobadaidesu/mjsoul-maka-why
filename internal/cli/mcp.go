package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	mjmcp "mjcap/internal/mcp"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpVersion = "0.1.0"

// runMCP serves stored games read-only: over stdio by default, or over
// loopback HTTP with a secret-path token when --listen is given. The server
// never connects to Chrome, the game, or any remote endpoint itself.
func runMCP(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("games-dir", filepath.Join("data", "games"), "private directory of stored games produced by mjcap decode --games-dir")
	listen := fs.String("listen", "", "serve streamable HTTP on this address instead of stdio (e.g. 127.0.0.1:8930)")
	tokenFile := fs.String("token-file", filepath.Join("data", "mcp-token"), "secret path token for HTTP mode; created (0600) on first use")
	exposed := fs.Bool("allow-nonlocal-listen", false, "explicitly permit a non-loopback --listen address")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "Usage: mjcap mcp [--games-dir DIR] [--listen ADDR --token-file FILE]")
		return 2
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	if *listen == "" {
		logger.Info("mcp server starting on stdio", "games_dir", *dir)
		err := mjmcp.New(*dir, mcpVersion).Run(ctx, &sdk.StdioTransport{})
		// A closed stdin means the MCP client disconnected; that is a normal
		// end of session, not a failure.
		if err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) {
			logger.Error("mcp server stopped", "error", err)
			return 1
		}
		return 0
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		logger.Error("invalid listen address", "error", err)
		return 2
	}
	if ip := net.ParseIP(host); !*exposed && (ip == nil || !ip.IsLoopback()) {
		logger.Error("refusing non-loopback listen address without --allow-nonlocal-listen; prefer a tunnel in front of loopback")
		return 2
	}
	token, err := loadOrCreateToken(*tokenFile)
	if err != nil {
		logger.Error("prepare token", "error", err)
		return 1
	}
	server := &http.Server{Addr: *listen, Handler: mjmcp.Handler(*dir, mcpVersion, token)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	logger.Info("mcp server starting on HTTP", "games_dir", *dir, "url", fmt.Sprintf("http://%s/%s/mcp", *listen, token))
	err = server.ListenAndServe()
	<-done
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("mcp http server stopped", "error", err)
		return 1
	}
	return 0
}

// loadOrCreateToken reads the secret path token, generating a random one with
// private permissions on first use.
func loadOrCreateToken(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		token := strings.TrimSpace(string(data))
		if len(token) < 32 {
			return "", fmt.Errorf("token in %s is shorter than 32 characters; delete it to regenerate", path)
		}
		return token, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		return "", err
	}
	return token, nil
}
