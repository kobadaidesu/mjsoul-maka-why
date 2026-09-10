package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mjcap/internal/capture"
	"mjcap/internal/decode"
	"mjcap/internal/liqi"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestReuseLoginIDsPrefersNewestAndSkipsExcluded(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		mod := time.Now().Add(-age)
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldest := write("a.jsonl", 3*time.Hour)
	newest := write("b.jsonl", time.Hour)
	excluded := write("c.jsonl", 0)
	visited := []string{}
	got, source := reuseLoginIDs(dir, excluded, func(path string) []uint64 {
		visited = append(visited, filepath.Base(path))
		if path == newest {
			return nil // no login here; fall through to the next candidate
		}
		return []uint64{42}
	})
	if len(got) != 1 || got[0] != 42 || source != oldest {
		t.Fatalf("ids=%v source=%s", got, source)
	}
	if len(visited) != 2 || visited[0] != "b.jsonl" || visited[1] != "a.jsonl" {
		t.Fatalf("visit order %v; want newest first without the excluded file", visited)
	}
}

// loginFixture mirrors the synthetic schema and measured envelope layout used
// by the decode tests (testdata/README.md); no real protocol bytes involved.
func loginFixture(t *testing.T) (*decode.Names, *liqi.Metadata, *liqi.Resource) {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/liqi/gamerecord.json")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := liqi.Build(raw)
	if err != nil {
		t.Fatal(err)
	}
	r := &liqi.Resource{Data: raw, Registry: reg, Metadata: liqi.Metadata{GameVersion: "synthetic-game", ResourceVersion: "synthetic-schema", SHA256: fmt.Sprintf("%x", sha256.Sum256(raw))}}
	p := decode.Profile{
		SchemaVersion: 1, EvidenceLevel: "CONFIRMED",
		EvidenceRef:     "SYNTHETIC fixture only; not evidence of a Mahjong Soul protocol",
		GameVersion:     r.Metadata.GameVersion,
		ResourceVersion: r.Metadata.ResourceVersion,
		SHA256:          r.Metadata.SHA256,
		WrapperMessage:  ".lq.Wrapper", NameField: "name", DataField: "data",
		TypeOffset: 0, Request: 2, Response: 3,
		RPCWrapperOffset: 3, NumberOffset: 1, NumberBytes: 2, ByteOrder: "little",
	}
	n, err := decode.NewNames(p, r)
	if err != nil {
		t.Fatal(err)
	}
	return n, &r.Metadata, r
}

func wrapLogin(t *testing.T, r *liqi.Resource, name string, payload []byte) []byte {
	t.Helper()
	md, err := r.Registry.Message(".lq.Wrapper")
	if err != nil {
		t.Fatal(err)
	}
	w := dynamicpb.NewMessage(md)
	if name != "" {
		w.Set(md.Fields().ByName("name"), protoreflect.ValueOfString(name))
	}
	w.Set(md.Fields().ByName("data"), protoreflect.ValueOfBytes(payload))
	out, err := proto.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func writeLoginCapture(t *testing.T, path string, r *liqi.Resource, accountID uint64) {
	t.Helper()
	md, err := r.Registry.Message(".lq.ResLogin")
	if err != nil {
		t.Fatal(err)
	}
	res := dynamicpb.NewMessage(md)
	res.Set(md.Fields().ByName("account_id"), protoreflect.ValueOfUint32(uint32(accountID)))
	resData, err := proto.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	// Envelope per the synthetic profile: type byte, 2-byte little-endian
	// request number, then the Wrapper bytes.
	req := append([]byte{2, 1, 0}, wrapLogin(t, r, oauth2LoginMethod, nil)...)
	resFrame := append([]byte{3, 1, 0}, wrapLogin(t, r, "", resData)...)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for i, e := range []capture.Event{
		{SchemaVersion: 1, Seq: 1, CapturedAt: time.Now(), Kind: "websocket", ConnectionID: "c", Direction: "sent", Opcode: 2, PayloadHex: hex.EncodeToString(req)},
		{SchemaVersion: 1, Seq: 2, CapturedAt: time.Now(), Kind: "websocket", ConnectionID: "c", Direction: "received", Opcode: 2, PayloadHex: hex.EncodeToString(resFrame)},
	} {
		if err := enc.Encode(e); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLoginIDsFromCapture(t *testing.T) {
	n, meta, r := loginFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "with-login.jsonl")
	writeLoginCapture(t, path, r, 222222)
	if got := loginIDsFromCapture(path, n, meta, r); len(got) != 1 || got[0] != 222222 {
		t.Fatalf("ids=%v", got)
	}
	// A capture bound to a different schema contributes nothing.
	other := filepath.Join(dir, "other-binding.jsonl")
	ctx, err := json.Marshal(map[string]any{"liqi": liqi.Metadata{GameVersion: "other", ResourceVersion: "other", SHA256: "other"}})
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(capture.Event{SchemaVersion: 1, Seq: 1, CapturedAt: time.Now(), Kind: "capture_context", Details: ctx})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, append(line, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if got := loginIDsFromCapture(other, n, meta, r); got != nil {
		t.Fatalf("mismatched binding yielded ids: %v", got)
	}
	if got := loginIDsFromCapture(filepath.Join(dir, "missing.jsonl"), n, meta, r); got != nil {
		t.Fatalf("missing file yielded ids: %v", got)
	}
}
