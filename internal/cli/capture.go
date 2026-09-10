package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"mjcap/internal/capture"
	"mjcap/internal/decode"
	"mjcap/internal/liqi"
)

type nameOptions struct {
	meta, profile string
	log           bool
	resource      *liqi.Resource
}

func (o *nameOptions) flags(fs *flag.FlagSet) {
	fs.StringVar(&o.meta, "liqi-meta", "", "exact local liqi binding metadata file")
	fs.StringVar(&o.profile, "protocol", "", "CONFIRMED envelope evidence profile; no built-in game format")
	fs.BoolVar(&o.log, "log-names", false, "log names when a confirmed protocol and exact schema are supplied")
}

func (o *nameOptions) load(logger *slog.Logger) (*decode.Names, *liqi.Metadata, error) {
	if (o.meta == "") != (o.profile == "") {
		return nil, nil, fmt.Errorf("--liqi-meta and --protocol must be supplied together")
	}
	if o.profile == "" {
		if o.log {
			logger.Warn("name logging unresolved: no confirmed envelope/schema; capturing raw only", "decode_status", "protocol_unverified")
		}
		return nil, nil, nil
	}
	r, err := liqi.OpenCachedMetadata(o.meta)
	if err != nil {
		return nil, nil, err
	}
	o.resource = r
	p, err := decode.LoadProfile(o.profile)
	if err != nil {
		return nil, nil, err
	}
	n, err := decode.NewNames(p, r)
	return n, &r.Metadata, err
}

func nameLogger(logger *slog.Logger, n *decode.Names, meta *liqi.Metadata, enabled bool) func(capture.Event) {
	return func(e capture.Event) {
		if e.BodyStatus != "" && e.BodyStatus != "stored" && e.BodyStatus != "not_selected" && e.BodyStatus != "awaiting_loading_finished" {
			logger.Warn("HTTP body observation status", "seq", e.Seq, "request_id", e.RequestID, "body_status", e.BodyStatus)
		}
		if e.Error != "" {
			logger.Warn("observation detail retained in private capture", "seq", e.Seq, "kind", e.Kind)
		}
		if n == nil {
			return
		}
		v := n.Observe(e)
		if !enabled || e.Kind != "websocket" {
			return
		}
		args := []any{"seq", v.Seq, "connection_id", v.ConnectionID, "direction", v.Direction, "decode_status", v.Status,
			"game_version", meta.GameVersion, "liqi_resource_version", meta.ResourceVersion, "liqi_sha256", meta.SHA256}
		if v.Number != nil {
			args = append(args, "message_number", *v.Number)
		}
		if v.Name != "" {
			args = append(args, "message_name", v.Name)
		}
		if v.ExpectedMessage != "" {
			args = append(args, "expected_message_name", v.ExpectedMessage)
		}
		if v.Reason != "" {
			args = append(args, "reason", v.Reason)
		}
		// No URL, headers, arbitrary CDP error, player data or payload prefix in
		// ordinary/debug logs. The private capture + seq is the diagnostic source.
		logger.Info("observed message", args...)
	}
}

func runCapture(ctx context.Context, args []string, stderr io.Writer) (code int) {
	return runCaptureWith(ctx, args, stderr, nil)
}

// runCaptureWith lets ingest inject its own logger (the friendly progress
// view); logger == nil keeps the plain text logs and the --debug flag.
func runCaptureWith(ctx context.Context, args []string, stderr io.Writer, logger *slog.Logger) (code int) {
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	endpoint := fs.String("endpoint", "http://127.0.0.1:9222", "existing Chrome debug endpoint")
	remote := fs.Bool("allow-remote-cdp", false, "explicitly permit a non-loopback debug endpoint")
	id := fs.String("target-id", "", "existing page target ID")
	host := fs.String("target-host", "game.mahjongsoul.com", "select the only existing page on this host")
	out := fs.String("out", "", "new private JSONL path (default: data/captures/capture-<UTC>.jsonl)")
	duration := fs.Duration("duration", 0, "stop after this duration; 0 waits for Ctrl-C")
	maxBody := fs.Int64("max-body-bytes", 32<<20, "maximum encoded response size eligible for body capture")
	maxTotal := fs.Int64("max-http-body-bytes", 128<<20, "HTTP body budget per process")
	pattern := fs.String("body-url-regexp", "", "also retain received bodies matching this regexp; no requests generated")
	debug := fs.Bool("debug", false, "enable debug logs (raw remains private)")
	var names nameOptions
	names.flags(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || *duration < 0 || *maxBody <= 0 || *maxTotal < *maxBody || *maxTotal > 1<<30 || (*host == "" && *id == "") {
		fmt.Fprintln(stderr, "invalid capture arguments or body budgets")
		return 2
	}
	policy := capture.BodyPolicy{MaxBodyBytes: *maxBody, MaxTotalBytes: *maxTotal}
	if *pattern != "" {
		var err error
		policy.URLPattern, err = regexp.Compile(*pattern)
		if err != nil {
			fmt.Fprintln(stderr, "invalid body URL regexp")
			return 2
		}
	}
	if logger == nil {
		level := slog.LevelInfo
		if *debug {
			level = slog.LevelDebug
		}
		logger = slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
	}
	n, meta, err := names.load(logger)
	if err != nil {
		logger.Error("load name evidence", "error", err)
		return 2
	}
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}
	targets, err := capture.Discover(ctx, *endpoint, *remote)
	if err != nil {
		logger.Error("discover Chrome", "error", err)
		return 1
	}
	target, err := capture.SelectTarget(targets, *id, *host, *endpoint, *remote)
	if err != nil {
		logger.Error("select existing tab", "error", err)
		return 1
	}
	if *out == "" {
		*out = filepath.Join("data", "captures", "capture-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".jsonl")
	}
	j, err := capture.NewJournal(*out)
	if err != nil {
		logger.Error("open private capture", "error", err)
		return 1
	}
	defer func() {
		if err := j.Close(); err != nil {
			logger.Error("close private capture", "error", err)
			code = 1
		}
	}()
	var evidence *decode.Profile
	if n != nil {
		p := n.Evidence()
		evidence = &p
	}
	details, err := json.Marshal(struct {
		Target       capture.Target  `json:"target"`
		Liqi         *liqi.Metadata  `json:"liqi,omitempty"`
		ProtocolFile string          `json:"protocol_file,omitempty"`
		Protocol     *decode.Profile `json:"protocol,omitempty"`
		MaxBody      int64           `json:"max_body_bytes"`
		MaxTotal     int64           `json:"max_http_body_bytes"`
		BodyPattern  string          `json:"body_url_regexp,omitempty"`
	}{target, meta, names.profile, evidence, *maxBody, *maxTotal, *pattern})
	if err != nil {
		logger.Error("encode capture context", "error", err)
		return 1
	}
	if _, err := j.Append(capture.Event{Kind: "capture_context", Details: details}); err != nil {
		logger.Error("save capture context", "error", err)
		return 1
	}
	logger.Info("private capture opened", "path", *out, "target_id", target.ID)
	err = capture.Observe(ctx, target, j, policy, nameLogger(logger, n, meta, names.log), func() { logger.Info("capture ready: perform replay/MAKA actions manually", "path", *out) })
	if err != nil {
		logger.Error("capture interrupted; saved raw retained", "error", err, "path", *out)
		return 1
	}
	logger.Info("capture stopped", "path", *out)
	return 0
}

func runInspect(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var names nameOptions
	names.flags(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "Usage: mjcap inspect [--log-names --liqi-meta FILE --protocol FILE] CAPTURE.jsonl")
		return 2
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	n, meta, err := names.load(logger)
	if err != nil {
		logger.Error("load name evidence", "error", err)
		return 2
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		logger.Error("open capture", "error", err)
		return 1
	}
	callback := nameLogger(logger, n, meta, names.log)
	counts := map[string]int{}
	var last uint64
	err = capture.Replay(f, func(e capture.Event) error {
		if err := checkCaptureBinding(e, n, meta); err != nil {
			return err
		}
		counts[e.Kind]++
		last = e.Seq
		callback(e)
		return nil
	})
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		logger.Error("inspect capture", "error", errors.Join(err, closeErr))
		return 1
	}
	logger.Info("offline capture summary", "events_by_kind", counts, "last_seq", last)
	return 0
}

func checkCaptureBinding(e capture.Event, n *decode.Names, meta *liqi.Metadata) error {
	if e.Kind != "capture_context" || n == nil {
		return nil
	}
	var binding struct {
		Liqi     *liqi.Metadata  `json:"liqi"`
		Protocol *decode.Profile `json:"protocol"`
	}
	if err := json.Unmarshal(e.Details, &binding); err != nil {
		return fmt.Errorf("invalid private capture context")
	}
	if m := binding.Liqi; m != nil && (meta == nil || m.SHA256 != meta.SHA256 || m.GameVersion != meta.GameVersion || m.ResourceVersion != meta.ResourceVersion) {
		return fmt.Errorf("capture liqi binding does not match supplied schema")
	}
	if binding.Protocol != nil && !decode.EquivalentProfiles(*binding.Protocol, n.Evidence()) {
		return fmt.Errorf("capture envelope binding does not match supplied protocol")
	}
	return nil
}
