// Package store persists normalized game reconstructions under a private
// games directory. Files carry a schema version and are written atomically;
// the package never touches the network or the game.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"mjcap/internal/extract"
)

const SchemaVersion = 1

// File is one stored game. Game contents contain seats, tiles, scores and
// MAKA values only; player identities are never stored here.
type File struct {
	SchemaVersion int          `json:"schema_version"`
	CapturedAt    time.Time    `json:"captured_at"`
	Game          extract.Game `json:"game"`
}

var uuidPattern = regexp.MustCompile(`^[0-9A-Za-z-]{8,128}$`)

func gamePath(dir, uuid string) (string, error) {
	if !uuidPattern.MatchString(uuid) {
		return "", fmt.Errorf("game uuid %q is not a safe filename", uuid)
	}
	return filepath.Join(dir, uuid+".json"), nil
}

// Save writes the game atomically (temp file, fsync, rename, directory sync)
// so interrupted writes never leave a broken JSON behind. Re-saving the same
// uuid replaces the previous reconstruction in one step.
func Save(dir string, f File) (path string, err error) {
	f.SchemaVersion = SchemaVersion
	if f.Game.UUID == "" || f.CapturedAt.IsZero() {
		return "", fmt.Errorf("stored game requires uuid and captured_at")
	}
	path, err = gamePath(dir, f.Game.UUID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create games directory: %w", err)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode game: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".game-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary game file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("restrict game file mode: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write game: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("sync game: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close game: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("publish game: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return path, nil
}

// Load reads one stored game and refuses unknown schema versions instead of
// silently reinterpreting older files.
func Load(path string) (File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("read stored game: %w", err)
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, fmt.Errorf("parse stored game %s: %w", filepath.Base(path), err)
	}
	if f.SchemaVersion != SchemaVersion {
		return File{}, fmt.Errorf("stored game %s has schema_version %d; this build reads %d and does not migrate silently", filepath.Base(path), f.SchemaVersion, SchemaVersion)
	}
	if f.Game.UUID == "" {
		return File{}, fmt.Errorf("stored game %s lacks a game uuid", filepath.Base(path))
	}
	return f, nil
}

// LoadByUUID loads dir/{uuid}.json after validating the uuid shape.
func LoadByUUID(dir, uuid string) (File, error) {
	path, err := gamePath(dir, uuid)
	if err != nil {
		return File{}, err
	}
	return Load(path)
}

// List loads every stored game, newest capture first. Unreadable files are
// reported as errors rather than skipped silently.
func List(dir string) ([]File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list games directory: %w", err)
	}
	var out []File
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		f, err := Load(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CapturedAt.After(out[j].CapturedAt) })
	return out, nil
}
