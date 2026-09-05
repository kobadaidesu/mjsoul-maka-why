package decode

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"mjcap/internal/capture"
	"mjcap/internal/liqi"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func fakeWS(direction string, payload []byte) capture.Event {
	return capture.Event{Seq: 1, Kind: "websocket", ConnectionID: "conn", Direction: direction, Opcode: 2, PayloadHex: hex.EncodeToString(payload)}
}

// gameFixture mirrors the measured .lq message shapes with synthetic values
// only (testdata/README.md). The profile mirrors the measured lobby envelope
// with notify declared unobserved.
func gameFixture(t *testing.T) (*Decoder, *liqi.Resource) {
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
	p := Profile{
		SchemaVersion: 1, EvidenceLevel: "CONFIRMED",
		EvidenceRef:     "SYNTHETIC fixture only; not evidence of a Mahjong Soul protocol",
		GameVersion:     r.Metadata.GameVersion,
		ResourceVersion: r.Metadata.ResourceVersion,
		SHA256:          r.Metadata.SHA256,
		WrapperMessage:  ".lq.Wrapper", NameField: "name", DataField: "data",
		TypeOffset: 0, Request: 2, Response: 3,
		RPCWrapperOffset: 3, NumberOffset: 1, NumberBytes: 2, ByteOrder: "little",
	}
	d, err := NewDecoder(p, r)
	if err != nil {
		t.Fatal(err)
	}
	return d, r
}

func message(t *testing.T, r *liqi.Resource, name string, set func(protoreflect.Message)) *dynamicpb.Message {
	t.Helper()
	md, err := r.Registry.Message(name)
	if err != nil {
		t.Fatal(err)
	}
	m := dynamicpb.NewMessage(md)
	if set != nil {
		set(m)
	}
	return m
}

func setField(t *testing.T, m protoreflect.Message, name string, v any) {
	t.Helper()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		t.Fatalf("fixture lacks field %s", name)
	}
	switch v := v.(type) {
	case string:
		m.Set(fd, protoreflect.ValueOfString(v))
	case []byte:
		m.Set(fd, protoreflect.ValueOfBytes(v))
	case uint64:
		if fd.Kind() == protoreflect.Uint32Kind || fd.Kind() == protoreflect.Fixed32Kind {
			m.Set(fd, protoreflect.ValueOfUint32(uint32(v)))
		} else {
			m.Set(fd, protoreflect.ValueOfUint64(v))
		}
	case bool:
		m.Set(fd, protoreflect.ValueOfBool(v))
	case []string:
		list := m.Mutable(fd).List()
		for _, s := range v {
			list.Append(protoreflect.ValueOfString(s))
		}
	case []int32:
		list := m.Mutable(fd).List()
		for _, n := range v {
			list.Append(protoreflect.ValueOfInt32(n))
		}
	case []uint32:
		list := m.Mutable(fd).List()
		for _, n := range v {
			list.Append(protoreflect.ValueOfUint32(n))
		}
	default:
		t.Fatalf("unsupported fixture value %T", v)
	}
}

func wrap(t *testing.T, r *liqi.Resource, name string, payload proto.Message) []byte {
	t.Helper()
	data, err := proto.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	w := message(t, r, ".lq.Wrapper", func(m protoreflect.Message) {
		setField(t, m, "name", name)
		setField(t, m, "data", data)
	})
	out, err := proto.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func resGameRecord(t *testing.T, r *liqi.Resource, detail proto.Message) *dynamicpb.Message {
	t.Helper()
	wrapped := wrap(t, r, ".lq.GameDetailRecords", detail)
	return message(t, r, ".lq.ResGameRecord", func(m protoreflect.Message) {
		setField(t, m, "data", wrapped)
	})
}

func gameAction(t *testing.T, r *liqi.Resource, name string, payload proto.Message) protoreflect.Value {
	t.Helper()
	action := message(t, r, ".lq.GameAction", func(m protoreflect.Message) {
		setField(t, m, "type", uint64(1))
		setField(t, m, "result", wrap(t, r, name, payload))
	})
	return protoreflect.ValueOfMessage(action)
}

func TestGameDetailActions(t *testing.T) {
	d, r := gameFixture(t)
	newRound := message(t, r, ".lq.RecordNewRound", func(m protoreflect.Message) {
		setField(t, m, "chang", uint64(1))
		setField(t, m, "ju", uint64(1))
	})
	deal := message(t, r, ".lq.RecordDealTile", func(m protoreflect.Message) {
		setField(t, m, "seat", uint64(2))
		setField(t, m, "tile", "3p")
	})
	detail := message(t, r, ".lq.GameDetailRecords", func(m protoreflect.Message) {
		setField(t, m, "version", uint64(999))
		list := m.Mutable(m.Descriptor().Fields().ByName("actions")).List()
		list.Append(gameAction(t, r, ".lq.RecordNewRound", newRound))
		list.Append(gameAction(t, r, ".lq.RecordDealTile", deal))
		list.Append(gameAction(t, r, ".lq.RecordUnknownFuture", deal))
		userInput := message(t, r, ".lq.GameAction", func(a protoreflect.Message) { setField(t, a, "type", uint64(2)) })
		list.Append(protoreflect.ValueOfMessage(userInput))
	})
	out, err := d.GameDetailActions(resGameRecord(t, r, detail))
	if err != nil {
		t.Fatal(err)
	}
	if out.Version != 999 || len(out.Actions) != 4 {
		t.Fatalf("got %+v", out)
	}
	if out.Actions[0].Name != ".lq.RecordNewRound" || out.Actions[0].Status != "decoded" || out.Actions[0].Message == nil {
		t.Fatalf("new round action %+v", out.Actions[0])
	}
	if tile, _ := StringField(out.Actions[1].Message, "tile"); tile != "3p" {
		t.Fatalf("deal tile lost: %+v", out.Actions[1])
	}
	if out.Actions[2].Status != "unknown_message" || out.Actions[2].Name != ".lq.RecordUnknownFuture" || out.Actions[2].Message != nil {
		t.Fatalf("unknown action must stay raw: %+v", out.Actions[2])
	}
	if out.Actions[3].Status != "no_result" {
		t.Fatalf("user input action %+v", out.Actions[3])
	}
}

func TestGameDetailRefusesUnverifiedLayouts(t *testing.T) {
	d, r := gameFixture(t)
	// data_url delivery was never captured.
	byURL := message(t, r, ".lq.ResGameRecord", func(m protoreflect.Message) {
		setField(t, m, "data_url", "https://example.invalid/record")
	})
	if _, err := d.GameDetailActions(byURL); err == nil {
		t.Fatal("data_url layout accepted without measurement")
	}
	// records-based layout was never captured.
	records := message(t, r, ".lq.GameDetailRecords", func(m protoreflect.Message) {
		list := m.Mutable(m.Descriptor().Fields().ByName("records")).List()
		list.Append(protoreflect.ValueOfBytes([]byte{1}))
	})
	if _, err := d.GameDetailActions(resGameRecord(t, r, records)); err == nil {
		t.Fatal("records layout accepted without measurement")
	}
	// An unexpected container name must not be decoded by guesswork.
	other := message(t, r, ".lq.ResGameRecord", func(m protoreflect.Message) {
		setField(t, m, "data", wrap(t, r, ".lq.SomethingElse", message(t, r, ".lq.RecordNoTile", nil)))
	})
	if _, err := d.GameDetailActions(other); err == nil {
		t.Fatal("unknown container accepted")
	}
}

func TestDecoderObserveDecodesResolvedPayloads(t *testing.T) {
	d, r := gameFixture(t)
	req := message(t, r, ".lq.ReqGameRecord", func(m protoreflect.Message) {
		setField(t, m, "game_uuid", "synthetic-uuid")
	})
	payload, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	w := message(t, r, ".lq.Wrapper", func(m protoreflect.Message) {
		setField(t, m, "name", ".lq.Lobby.fetchGameRecord")
		setField(t, m, "data", payload)
	})
	body, err := proto.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	frame := append([]byte{2, 9, 0}, body...)
	got := d.Observe(fakeWS("sent", frame))
	if got.Status != "resolved" || got.Message == nil {
		t.Fatalf("request not decoded: %+v", got.Observation)
	}
	if uuid, _ := StringField(got.Message, "game_uuid"); uuid != "synthetic-uuid" {
		t.Fatalf("request payload lost: %+v", got)
	}
}
