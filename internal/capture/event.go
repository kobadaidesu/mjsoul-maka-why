// Package capture observes existing Chrome network traffic. It has no Mahjong
// Soul protocol knowledge and cannot send game requests.
package capture

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const SchemaVersion = 1

// Event retains original CDP params alongside transport-neutral indexing.
// URLs, headers, payloads and errors here are private; do not log whole events.
type Event struct {
	SchemaVersion int             `json:"schema_version"`
	Seq           uint64          `json:"seq"`
	CapturedAt    time.Time       `json:"captured_at"`
	Kind          string          `json:"kind"`
	ConnectionID  string          `json:"connection_id,omitempty"`
	Direction     string          `json:"direction,omitempty"`
	Opcode        int64           `json:"opcode,omitempty"`
	PayloadHex    string          `json:"payload_hex,omitempty"`
	RequestID     string          `json:"request_id,omitempty"`
	URL           string          `json:"url,omitempty"`
	Status        int64           `json:"status,omitempty"`
	MIMEType      string          `json:"mime_type,omitempty"`
	ResourceType  string          `json:"resource_type,omitempty"`
	BodyBase64    *string         `json:"body_base64,omitempty"`
	BodyStatus    string          `json:"body_status,omitempty"`
	Error         string          `json:"error,omitempty"`
	CDPMethod     string          `json:"cdp_method,omitempty"`
	CDPID         int64           `json:"cdp_id,omitempty"`
	CDPParams     json.RawMessage `json:"cdp_params,omitempty"`
	CDPResult     json.RawMessage `json:"cdp_result,omitempty"`
	Details       json.RawMessage `json:"details,omitempty"`
}

// Journal is owned by one event loop. Append synchronizes each complete record
// before returning, so name logging can never precede raw persistence.
type Journal struct {
	file *os.File
	seq  uint64
}

func NewJournal(path string) (*Journal, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create private capture directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("capture parent must be a real private directory (0700)")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("create capture without overwriting: %w", err)
	}
	return &Journal{file: f}, nil
}

func (j *Journal) Append(e Event) (Event, error) {
	e.SchemaVersion = SchemaVersion
	e.Seq = j.seq + 1
	if e.CapturedAt.IsZero() {
		e.CapturedAt = time.Now().UTC()
	}
	if err := validateEvent(e); err != nil {
		return Event{}, err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return Event{}, fmt.Errorf("encode private capture: %w", err)
	}
	if _, err := j.file.Write(append(data, '\n')); err != nil {
		return Event{}, fmt.Errorf("write private capture: %w", err)
	}
	if err := j.file.Sync(); err != nil {
		return Event{}, fmt.Errorf("sync private capture: %w", err)
	}
	j.seq = e.Seq
	return e, nil
}

func (j *Journal) Close() error { return j.file.Close() }

func validateEvent(e Event) error {
	if e.SchemaVersion != SchemaVersion || e.Seq == 0 || e.CapturedAt.IsZero() || e.Kind == "" {
		return fmt.Errorf("invalid capture envelope or unsupported schema version")
	}
	if e.Kind == "websocket" {
		if e.ConnectionID == "" || (e.Direction != "sent" && e.Direction != "received") {
			return fmt.Errorf("invalid websocket event identifiers")
		}
		if _, err := hex.DecodeString(e.PayloadHex); err != nil {
			return fmt.Errorf("invalid websocket payload encoding")
		}
	}
	if e.BodyBase64 != nil {
		if _, err := base64.StdEncoding.DecodeString(*e.BodyBase64); err != nil {
			return fmt.Errorf("invalid HTTP body encoding")
		}
	}
	return nil
}

// Replay preserves seq order and refuses truncation, duplicate/out-of-order
// sequence numbers, and unknown schema versions. Unknown event kinds survive.
func Replay(r io.Reader, visit func(Event) error) error {
	reader := bufio.NewReader(r)
	var seq uint64
	for {
		line, err := readRecord(reader, 256<<20)
		if err == io.EOF && len(line) == 0 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("capture after seq %d: truncated record or read failure: %w", seq, err)
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return fmt.Errorf("parse capture after seq %d: %w", seq, err)
		}
		if err := validateEvent(e); err != nil {
			return fmt.Errorf("capture seq %d: %w", e.Seq, err)
		}
		if e.Seq <= seq {
			return fmt.Errorf("capture sequence is not strictly increasing at %d", e.Seq)
		}
		seq = e.Seq
		if err := visit(e); err != nil {
			return fmt.Errorf("visit capture seq %d: %w", seq, err)
		}
	}
}

func readRecord(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > limit {
			return nil, fmt.Errorf("capture record exceeds %d bytes", limit)
		}
		line = append(line, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return line, err
	}
}
