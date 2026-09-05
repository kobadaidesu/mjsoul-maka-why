package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mjcap/internal/extract"
)

func sample(uuid string, at time.Time) File {
	return File{
		CapturedAt: at,
		Game: extract.Game{
			UUID: uuid, Version: 1, Seats: 4,
			Rounds: []Round(nil), // filled below to keep the literal short
		},
	}
}

type Round = extract.Round

func TestSaveLoadListRoundtrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "games")
	older := sample("synthetic-uuid-a", time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC))
	older.Game.Rounds = []Round{{HandIndex: 0, Label: "東1局0本場", EndStatus: "hule"}}
	newer := sample("synthetic-uuid-b", time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC))
	newer.Game.Rounds = []Round{{HandIndex: 0, Label: "東1局0本場", EndStatus: "no_tile"}}
	for _, f := range []File{older, newer} {
		if _, err := Save(dir, f); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(filepath.Join(dir, "synthetic-uuid-a.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("stored game permissions: %v %v", info, err)
	}
	got, err := LoadByUUID(dir, "synthetic-uuid-a")
	if err != nil || got.SchemaVersion != SchemaVersion || got.Game.Rounds[0].Label != "東1局0本場" {
		t.Fatalf("roundtrip %+v %v", got, err)
	}
	all, err := List(dir)
	if err != nil || len(all) != 2 || all[0].Game.UUID != "synthetic-uuid-b" {
		t.Fatalf("list order %+v %v", all, err)
	}
	// Re-saving replaces atomically instead of accumulating files.
	older.Game.Version = 2
	if _, err := Save(dir, older); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("unexpected files: %v", entries)
	}
	if got, _ := LoadByUUID(dir, "synthetic-uuid-a"); got.Game.Version != 2 {
		t.Fatalf("replacement lost: %+v", got)
	}
}

func TestLoadRefusesOtherSchemaVersions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic-uuid-c.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":2,"captured_at":"2026-09-05T00:00:00Z","game":{"game_uuid":"synthetic-uuid-c"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("foreign schema accepted: %v", err)
	}
}

func TestSaveRejectsUnsafeUUIDs(t *testing.T) {
	dir := t.TempDir()
	for _, uuid := range []string{"", "../escape", "a/b", "short", strings.Repeat("x", 200)} {
		f := sample(uuid, time.Now())
		if _, err := Save(dir, f); err == nil {
			t.Fatalf("uuid %q accepted", uuid)
		}
	}
	if _, err := LoadByUUID(dir, "../escape"); err == nil {
		t.Fatal("path traversal accepted")
	}
}
