package decode

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"mjcap/internal/capture"
	"mjcap/internal/liqi"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func synthetic(t *testing.T) (Profile, *liqi.Resource) {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/liqi/wire.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := liqi.Build(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, err := LoadProfile("../../testdata/capture/protocol.json")
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	if p.SHA256 != hash {
		t.Fatal("synthetic schema hash drifted")
	}
	return p, &liqi.Resource{Data: raw, Registry: r, Metadata: liqi.Metadata{GameVersion: p.GameVersion, ResourceVersion: p.ResourceVersion, SHA256: hash}}
}

func frame(t *testing.T, p Profile, r *liqi.Resource, kind byte, number uint16, name string) string {
	t.Helper()
	m, err := r.Registry.Message(p.WrapperMessage)
	if err != nil {
		t.Fatal(err)
	}
	w := dynamicpb.NewMessage(m)
	if name != "" {
		w.Set(m.Fields().ByName(protoreflect.Name(p.NameField)), protoreflect.ValueOfString(name))
	}
	w.Set(m.Fields().ByName(protoreflect.Name(p.DataField)), protoreflect.ValueOfBytes([]byte{0, 255, 254}))
	data, err := proto.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	offset := p.RPCWrapperOffset
	notify := p.Notify != nil && kind == *p.Notify
	if notify {
		offset = *p.NotifyWrapperOffset
	}
	header := make([]byte, offset)
	header[p.TypeOffset] = kind
	if !notify {
		binary.LittleEndian.PutUint16(header[p.NumberOffset:], number)
	}
	return hex.EncodeToString(append(header, data...))
}

func TestNamesAndCorrelation(t *testing.T) {
	p, r := synthetic(t)
	n, err := NewNames(p, r)
	if err != nil {
		t.Fatal(err)
	}
	seq := uint64(0)
	check := func(conn, direction, payload, status, name, expected string) {
		t.Helper()
		seq++
		v := n.Observe(capture.Event{Seq: seq, Kind: "websocket", ConnectionID: conn, Direction: direction, Opcode: 2, PayloadHex: payload})
		if v.Status != status || v.Name != name || v.ExpectedMessage != expected {
			t.Fatalf("seq %d got %+v", seq, v)
		}
	}
	check("a", "received", frame(t, p, r, *p.Notify, 0, ".fixture.Notice"), "resolved", ".fixture.Notice", ".fixture.Notice")
	check("a", "sent", frame(t, p, r, p.Request, 7, ".fixture.Service.call"), "resolved", ".fixture.Service.call", ".fixture.Req")
	check("b", "sent", frame(t, p, r, p.Request, 7, ".fixture.Service.other"), "resolved", ".fixture.Service.other", ".fixture.Req")
	check("b", "received", frame(t, p, r, p.Response, 7, ""), "resolved", ".fixture.Service.other", ".fixture.Notice")
	check("a", "received", frame(t, p, r, p.Response, 7, ""), "resolved", ".fixture.Service.call", ".fixture.Res")
	check("a", "received", frame(t, p, r, p.Response, 7, ""), "type_unresolved", "", "")
	check("a", "sent", frame(t, p, r, p.Request, 8, ".fixture.Unknown.call"), "unknown_message", ".fixture.Unknown.call", "")
	check("a", "received", frame(t, p, r, p.Response, 8, ""), "unknown_message", ".fixture.Unknown.call", "")
	check("a", "sent", frame(t, p, r, p.Request, 9, ".fixture.Service.call"), "resolved", ".fixture.Service.call", ".fixture.Req")
	n.Observe(capture.Event{Kind: "websocket_close", ConnectionID: "a"})
	check("a", "received", frame(t, p, r, p.Response, 9, ""), "type_unresolved", "", "")
	check("a", "sent", frame(t, p, r, p.Request, 10, ".fixture.Service.call"), "resolved", ".fixture.Service.call", ".fixture.Req")
	check("a", "sent", frame(t, p, r, p.Request, 10, ".fixture.Service.other"), "ambiguous_request_number", "", "")
	check("a", "received", frame(t, p, r, p.Response, 10, ""), "type_unresolved", "", "")
	check("a", "received", frame(t, p, r, p.Response, 11, ".fixture.Res"), "resolved", ".fixture.Res", ".fixture.Res")
}

func TestInvalidFramesRemainNonfatal(t *testing.T) {
	p, r := synthetic(t)
	for _, tc := range []struct{ hex, status string }{
		{"", "short_frame"}, {"22", "short_frame"}, {"3301", "short_frame"}, {"ff", "unknown_frame_type"}, {"zz", "invalid_payload_hex"}, {"1100", "wrapper_decode_failed"},
	} {
		t.Run(tc.status+tc.hex, func(t *testing.T) {
			n, err := NewNames(p, r)
			if err != nil {
				t.Fatal(err)
			}
			e := capture.Event{Kind: "websocket", ConnectionID: "a", Direction: "received", Opcode: 2, PayloadHex: tc.hex}
			v := n.Observe(e)
			if v.Status != tc.status {
				t.Fatalf("got %+v", v)
			}
			if e.PayloadHex != tc.hex {
				t.Fatal("raw modified")
			}
		})
	}
	n, err := NewNames(p, r)
	if err != nil {
		t.Fatal(err)
	}
	orphan := n.Observe(capture.Event{Kind: "websocket", ConnectionID: "a", Direction: "received", Opcode: 2, PayloadHex: frame(t, p, r, p.Response, 5, "")})
	if orphan.Reason != "missing observed request" {
		t.Fatalf("orphan reason %+v", orphan)
	}
	bad := n.Observe(capture.Event{Kind: "websocket", ConnectionID: "a", Direction: "received", Opcode: 2, PayloadHex: frame(t, p, r, *p.Notify, 0, "private data\nnot a message")})
	if bad.Status != "invalid_message_name" || bad.Name != "" {
		t.Fatal("untrusted name escaped validation")
	}
}

func TestEvidenceRequired(t *testing.T) {
	p, r := synthetic(t)
	for _, change := range []func(*Profile){
		func(p *Profile) { p.EvidenceLevel = "CANDIDATE" }, func(p *Profile) { p.EvidenceRef = "" }, func(p *Profile) { p.SHA256 = "wrong" }, func(p *Profile) { p.GameVersion = "old" }, func(p *Profile) { p.Request = p.Response }, func(p *Profile) { p.RPCWrapperOffset = 1 }, func(p *Profile) { p.DataField = "missing" }, func(p *Profile) { p.NumberOffset = 0 },
		func(p *Profile) { p.Notify = nil }, func(p *Profile) { p.NotifyWrapperOffset = nil }, func(p *Profile) { p.Notify = &p.Request },
	} {
		copy := p
		change(&copy)
		if _, err := NewNames(copy, r); err == nil {
			t.Fatalf("unverified/mismatched profile accepted %+v", copy)
		}
	}
}

func TestNotifyDeclaredUnobserved(t *testing.T) {
	p, r := synthetic(t)
	notifyKind := *p.Notify
	p.Notify, p.NotifyWrapperOffset = nil, nil
	n, err := NewNames(p, r)
	if err != nil {
		t.Fatal(err)
	}
	// A frame carrying the (undeclared) notify byte must stay raw, while
	// request/response evidence keeps resolving.
	v := n.Observe(capture.Event{Seq: 1, Kind: "websocket", ConnectionID: "a", Direction: "received", Opcode: 2, PayloadHex: hex.EncodeToString([]byte{notifyKind, 0, 0, 0})})
	if v.Status != "unknown_frame_type" || v.Name != "" {
		t.Fatalf("undeclared notify decoded: %+v", v)
	}
	req := n.Observe(capture.Event{Seq: 2, Kind: "websocket", ConnectionID: "a", Direction: "sent", Opcode: 2, PayloadHex: frame(t, p, r, p.Request, 3, ".fixture.Service.call")})
	if req.Status != "resolved" || req.ExpectedMessage != ".fixture.Req" {
		t.Fatalf("request no longer resolves: %+v", req)
	}
}

func TestEquivalentProfiles(t *testing.T) {
	p, _ := synthetic(t)
	q := p
	q.EvidenceRef = "different reference"
	if !EquivalentProfiles(p, q) {
		t.Fatal("evidence reference must not affect binding equivalence")
	}
	q.Notify, q.NotifyWrapperOffset = nil, nil
	if EquivalentProfiles(p, q) {
		t.Fatal("notify evidence difference not detected")
	}
	other := byte(*p.Notify + 1)
	q = p
	q.Notify = &other
	if EquivalentProfiles(p, q) {
		t.Fatal("notify value difference not detected")
	}
}

func TestProfileMissingAndDuplicateProperties(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/capture/protocol.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{
		bytes.Replace(raw, []byte(`"type_offset": 0,`), nil, 1),
		bytes.Replace(raw, []byte(`"type_offset": 0,`), []byte(`"type_offset": 0, "type_offset": 1,`), 1),
		bytes.Replace(raw, []byte(`"type_offset": 0,`), []byte(`"type_offset": null,`), 1),
		bytes.Replace(raw, []byte(`"notify": 17,`), []byte(`"notify": null,`), 1),
	} {
		path := filepath.Join(t.TempDir(), "profile.json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadProfile(path); err == nil {
			t.Fatal("ambiguous/missing property accepted")
		}
	}
	// Both notify properties explicitly null is a valid declared-unobserved state.
	unobserved := bytes.Replace(bytes.Replace(raw, []byte(`"notify": 17,`), []byte(`"notify": null,`), 1), []byte(`"notify_wrapper_offset": 1,`), []byte(`"notify_wrapper_offset": null,`), 1)
	path := filepath.Join(t.TempDir(), "unobserved.json")
	if err := os.WriteFile(path, unobserved, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadProfile(path)
	if err != nil || p.Notify != nil || p.NotifyWrapperOffset != nil {
		t.Fatalf("declared-unobserved notify rejected: %v %+v", err, p)
	}
}
