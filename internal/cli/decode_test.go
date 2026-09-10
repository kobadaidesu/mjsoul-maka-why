package cli

import (
	"os"
	"testing"

	"mjcap/internal/liqi"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func recordHead(t *testing.T, seats map[uint64]uint64) protoreflect.Message {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/liqi/gamerecord.json")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := liqi.Build(raw)
	if err != nil {
		t.Fatal(err)
	}
	md, err := reg.Message(".lq.RecordGame")
	if err != nil {
		t.Fatal(err)
	}
	head := dynamicpb.NewMessage(md)
	accountsField := md.Fields().ByName("accounts")
	list := head.Mutable(accountsField).List()
	for id, seat := range seats {
		acc := dynamicpb.NewMessage(accountsField.Message())
		acc.Set(accountsField.Message().Fields().ByName("account_id"), protoreflect.ValueOfUint32(uint32(id)))
		acc.Set(accountsField.Message().Fields().ByName("seat"), protoreflect.ValueOfUint32(uint32(seat)))
		list.Append(protoreflect.ValueOfMessage(acc))
	}
	return head
}

func ids(v ...uint64) map[uint64]bool {
	m := map[uint64]bool{}
	for _, id := range v {
		m[id] = true
	}
	return m
}

func TestSelfSeatFromHead(t *testing.T) {
	head := recordHead(t, map[uint64]uint64{111111: 0, 222222: 2, 333333: 3})
	if s := selfSeatFromHead(head, ids(222222)); s == nil || *s != 2 {
		t.Fatalf("matched account not resolved: %v", s)
	}
	if s := selfSeatFromHead(head, ids(999999)); s != nil {
		t.Fatalf("unknown account produced a seat: %v", s)
	}
	// No login observed anywhere: never guess.
	if s := selfSeatFromHead(head, ids()); s != nil {
		t.Fatalf("empty id set matched: %v", s)
	}
	// Two known accounts in the same game (shared captures directory) are
	// ambiguous; the seat must stay unknown rather than guessed.
	if s := selfSeatFromHead(head, ids(111111, 333333)); s != nil {
		t.Fatalf("ambiguous accounts produced a seat: %v", s)
	}
	if s := selfSeatFromHead(head, ids(111111, 999999)); s == nil || *s != 0 {
		t.Fatalf("single known account with an unknown extra not resolved: %v", s)
	}
}
