package cli

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"mjcap/internal/capture"
	"mjcap/internal/decode"
	"mjcap/internal/liqi"
)

// reuseLoginIDs finds the newest capture in dir (excluding the one being
// decoded) whose login response yields account ids, using fromFile to read
// each candidate. The ids exist in memory only; callers must never store or
// log them (AGENTS.md privacy rules) — only the seat number derived from a
// match may be kept.
func reuseLoginIDs(dir, exclude string, fromFile func(path string) []uint64) ([]uint64, string) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, ""
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	var files []candidate
	for _, m := range matches {
		if filepath.Clean(m) == filepath.Clean(exclude) {
			continue
		}
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		files = append(files, candidate{m, info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for _, f := range files {
		if ids := fromFile(f.path); len(ids) > 0 {
			return ids, f.path
		}
	}
	return nil, ""
}

// loginIDsFromCapture replays one capture only until a login response is
// found and returns its account id. Captures bound to a different schema or
// envelope profile are skipped entirely; a capture that fails mid-replay
// still contributes nothing beyond what was decoded before the failure.
func loginIDsFromCapture(path string, n *decode.Names, meta *liqi.Metadata, r *liqi.Resource) []uint64 {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	d, err := decode.NewDecoder(n.Evidence(), r)
	if err != nil {
		return nil
	}
	var ids []uint64
	errFound := errors.New("login response found")
	_ = capture.Replay(f, func(e capture.Event) error {
		if err := checkCaptureBinding(e, n, meta); err != nil {
			return err
		}
		frame := d.Observe(e)
		if frame.Kind != "response" || frame.Name != oauth2LoginMethod || frame.Message == nil {
			return nil
		}
		if id, err := decode.UintField(frame.Message, "account_id"); err == nil && id != 0 {
			ids = append(ids, id)
			return errFound
		}
		return nil
	})
	return ids
}
