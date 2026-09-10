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
		// "disabled" is the normal state under --http-bodies=false, not a
		// per-request problem, so it must not warn on every HTTP response.
		if e.BodyStatus != "" && e.BodyStatus != "stored" && e.BodyStatus != "not_selected" && e.BodyStatus != "awaiting_loading_finished" && e.BodyStatus != "disabled" {
			logger.Warn("HTTP body observation status", cliEventKey, cliEventBodyStatus, "seq", e.Seq, "request_id", e.RequestID, "body_status", e.BodyStatus)
		}
		if e.Error != "" {
			logger.Warn("observation detail retained in private capture", cliEventKey, cliEventObservationDetail, "seq", e.Seq, "kind", e.Kind)
		}
		if n == nil {
			return
		}
		v := n.Observe(e)
		if !enabled || e.Kind != "websocket" {
			return
		}
		args := []any{cliEventKey, cliEventObservedMessage, "seq", v.Seq, "connection_id", v.ConnectionID, "direction", v.Direction, "decode_status", v.Status,
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

// captureOptions is the parsed CLI surface of the capture subcommand. It is
// produced only by parseCaptureFlags so tests exercise the real flag parsing
// (defaults included) rather than a parallel construction path.
type captureOptions struct {
	endpoint, targetID, host, out string
	remote, debug, httpBodies     bool
	duration                      time.Duration
	maxBody, maxTotal             int64
	pattern                       string
	policy                        capture.BodyPolicy
	names                         nameOptions
}

// parseCaptureFlags returns nil with the exit code when parsing stops
// (--help or invalid arguments).
func parseCaptureFlags(args []string, stderr io.Writer) (*captureOptions, int) {
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	o := &captureOptions{}
	fs.StringVar(&o.endpoint, "endpoint", "http://127.0.0.1:9222", "existing Chrome debug endpoint")
	fs.BoolVar(&o.remote, "allow-remote-cdp", false, "explicitly permit a non-loopback debug endpoint")
	fs.StringVar(&o.targetID, "target-id", "", "existing page target ID")
	fs.StringVar(&o.host, "target-host", "game.mahjongsoul.com", "select the only existing page on this host")
	fs.StringVar(&o.out, "out", "", "new private JSONL path (default: data/captures/capture-<UTC>.jsonl)")
	fs.DurationVar(&o.duration, "duration", 0, "stop after this duration; 0 waits for Ctrl-C")
	fs.Int64Var(&o.maxBody, "max-body-bytes", 32<<20, "maximum encoded response size eligible for body capture")
	fs.Int64Var(&o.maxTotal, "max-http-body-bytes", 128<<20, "HTTP body budget per process")
	fs.StringVar(&o.pattern, "body-url-regexp", "", "also retain received bodies matching this regexp; no requests generated")
	fs.BoolVar(&o.httpBodies, "http-bodies", true, "request selected HTTP response bodies; false records metadata only (ingest's default)")
	fs.BoolVar(&o.debug, "debug", false, "enable debug logs (raw remains private)")
	o.names.flags(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, 0
		}
		return nil, 2
	}
	if fs.NArg() != 0 || o.duration < 0 || (o.host == "" && o.targetID == "") {
		fmt.Fprintln(stderr, "invalid capture arguments or body budgets")
		return nil, 2
	}
	policy, err := captureBodyPolicy(o.maxBody, o.maxTotal, o.pattern, o.httpBodies)
	if err != nil {
		fmt.Fprintln(stderr, "invalid capture arguments or body budgets:", err)
		return nil, 2
	}
	o.policy = policy
	return o, 0
}

// runCaptureWith lets ingest inject its own logger (the friendly progress
// view); logger == nil keeps the plain text logs and the --debug flag.
func runCaptureWith(ctx context.Context, args []string, stderr io.Writer, logger *slog.Logger) (code int) {
	opts, code := parseCaptureFlags(args, stderr)
	if opts == nil {
		return code
	}
	if logger == nil {
		level := slog.LevelInfo
		if opts.debug {
			level = slog.LevelDebug
		}
		logger = slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
	}
	n, meta, err := opts.names.load(logger)
	if err != nil {
		logger.Error("load name evidence", "error", err)
		return 2
	}
	if opts.duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.duration)
		defer cancel()
	}
	targets, err := capture.Discover(ctx, opts.endpoint, opts.remote)
	if err != nil {
		logger.Error("discover Chrome", cliEventKey, cliEventChromeDiscover, "error", err)
		return 1
	}
	target, err := capture.SelectTarget(targets, opts.targetID, opts.host, opts.endpoint, opts.remote)
	if err != nil {
		logger.Error("select existing tab", cliEventKey, cliEventTabSelect, "error", err)
		return 1
	}
	if opts.out == "" {
		opts.out = filepath.Join("data", "captures", "capture-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".jsonl")
	}
	j, err := capture.NewJournal(opts.out)
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
	details, err := encodeCaptureContext(target, meta, opts.names.profile, evidence, opts.maxBody, opts.maxTotal, opts.pattern, opts.httpBodies)
	if err != nil {
		logger.Error("encode capture context", "error", err)
		return 1
	}
	if _, err := j.Append(capture.Event{Kind: "capture_context", Details: details}); err != nil {
		logger.Error("save capture context", "error", err)
		return 1
	}
	logger.Info("private capture opened", cliEventKey, cliEventCaptureOpened, "path", opts.out, "target_id", target.ID)
	err = capture.Observe(ctx, target, j, opts.policy, nameLogger(logger, n, meta, opts.names.log), func() {
		logger.Info("capture ready: perform replay/MAKA actions manually", cliEventKey, cliEventCaptureReady, "path", opts.out)
	})
	if err != nil {
		logger.Error("capture interrupted; saved raw retained", cliEventKey, cliEventCaptureInterrupted, "error", err, "path", opts.out)
		return 1
	}
	logger.Info("capture stopped", cliEventKey, cliEventCaptureStopped, "path", opts.out)
	return 0
}

// captureBodyPolicy maps the capture CLI flags onto the observer policy.
// --http-bodies=false becomes the opt-out DisableHTTPBodies so a zero-value
// BodyPolicy keeps its historical meaning. Budget validation is unchanged.
func captureBodyPolicy(maxBody, maxTotal int64, pattern string, httpBodies bool) (capture.BodyPolicy, error) {
	if maxBody <= 0 || maxTotal < maxBody || maxTotal > 1<<30 {
		return capture.BodyPolicy{}, fmt.Errorf("body budgets out of range")
	}
	policy := capture.BodyPolicy{MaxBodyBytes: maxBody, MaxTotalBytes: maxTotal, DisableHTTPBodies: !httpBodies}
	if pattern != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return capture.BodyPolicy{}, fmt.Errorf("invalid body URL regexp")
		}
		policy.URLPattern = re
	}
	return policy, nil
}

// encodeCaptureContext records the capture settings alongside the evidence
// binding. http_bodies is written even when false so later inspection can
// tell an intentionally body-less capture from a pre-flag one.
func encodeCaptureContext(target capture.Target, meta *liqi.Metadata, protocolFile string, evidence *decode.Profile, maxBody, maxTotal int64, pattern string, httpBodies bool) ([]byte, error) {
	return json.Marshal(struct {
		Target       capture.Target  `json:"target"`
		Liqi         *liqi.Metadata  `json:"liqi,omitempty"`
		ProtocolFile string          `json:"protocol_file,omitempty"`
		Protocol     *decode.Profile `json:"protocol,omitempty"`
		MaxBody      int64           `json:"max_body_bytes"`
		MaxTotal     int64           `json:"max_http_body_bytes"`
		BodyPattern  string          `json:"body_url_regexp,omitempty"`
		HTTPBodies   bool            `json:"http_bodies"`
	}{target, meta, protocolFile, evidence, maxBody, maxTotal, pattern, httpBodies})
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
