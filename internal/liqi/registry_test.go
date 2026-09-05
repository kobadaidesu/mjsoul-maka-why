package liqi

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/liqi/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFeatureRegistryRoundTrip(t *testing.T) {
	r, err := Build(fixture(t, "features.json"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Counts != (Counts{Files: 2, Messages: 6, Enums: 2}) {
		t.Fatalf("counts: %+v", r.Counts)
	}
	desc, err := r.Message(".synthetic.Packet")
	if err != nil {
		t.Fatal(err)
	}
	message := dynamicpb.NewMessage(desc)
	if err := protojson.Unmarshal(fixture(t, "packet.json"), message); err != nil {
		t.Fatal(err)
	}
	explicit := desc.Fields().ByName("explicit")
	if !message.Has(explicit) || !explicit.HasOptionalKeyword() || !explicit.ContainingOneof().IsSynthetic() {
		t.Fatal("optional zero must have presence")
	}
	if got := message.WhichOneof(desc.Oneofs().ByName("choice")); got.Name() != "text" {
		t.Fatal("oneof not selected")
	}
	if !desc.Fields().ByName("labels").IsMap() || desc.Fields().ByName("nums").IsPacked() {
		t.Fatal("map or packed=false lost")
	}
	if string(desc.Fields().ByName("shared").Message().FullName()) != "common.Shared" {
		t.Fatal("cross-package reference lost")
	}
	raw, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	unknown := protowire.AppendTag(nil, 500, protowire.BytesType)
	unknown = protowire.AppendBytes(unknown, []byte{0, 255, 13})
	decoded := dynamicpb.NewMessage(desc)
	if err := proto.Unmarshal(append(raw, unknown...), decoded); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.GetUnknown(), unknown) {
		t.Fatal("unknown fields discarded")
	}
	encoded, err := proto.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	again := dynamicpb.NewMessage(desc)
	if err := proto.Unmarshal(encoded, again); err != nil || !proto.Equal(again, decoded) {
		t.Fatalf("binary round trip failed: %v", err)
	}
	decoded.SetUnknown(nil)
	if !proto.Equal(message, decoded) {
		t.Fatal("known fields changed")
	}
	service, err := r.Files.FindDescriptorByName("synthetic.Endpoint")
	if err != nil || service.(protoreflect.ServiceDescriptor).Methods().Get(0).Input().FullName() != desc.FullName() {
		t.Fatalf("service type resolution: %v", err)
	}
}

func TestEveryScalar(t *testing.T) {
	// A synthetic field per standard protobuf scalar, including max/min integers.
	values := map[string]string{"double": "1.25", "float": "-2.5", "int32": "-2147483648", "uint32": "4294967295", "int64": `"-9223372036854775808"`, "uint64": `"18446744073709551615"`, "sint32": "-3", "sint64": `"-4"`, "fixed32": "5", "fixed64": `"6"`, "sfixed32": "-7", "sfixed64": `"-8"`, "bool": "true", "string": `"hello"`, "bytes": `"AP8="`}
	for scalar, value := range values {
		t.Run(scalar, func(t *testing.T) {
			schema := fmt.Sprintf(`{"nested":{"M":{"fields":{"v":{"type":%q,"id":1}}}}}`, scalar)
			r, err := Build([]byte(schema))
			if err != nil {
				t.Fatal(err)
			}
			d, err := r.Message("M")
			if err != nil {
				t.Fatal(err)
			}
			m := dynamicpb.NewMessage(d)
			if err := protojson.Unmarshal([]byte(`{"v":`+value+`}`), m); err != nil {
				t.Fatal(err)
			}
			raw, err := proto.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			out := dynamicpb.NewMessage(d)
			if err := proto.Unmarshal(raw, out); err != nil || !proto.Equal(m, out) {
				t.Fatalf("scalar roundtrip: %v", err)
			}
		})
	}
}

func TestInvalidSchemas(t *testing.T) {
	cases := []struct{ name, json, contains string }{
		{"empty", ``, "size"},
		{"duplicateJSON", `{"nested":{},"nested":{}}`, "duplicate"},
		{"trailing", `{"nested":{}} {}`, "end of JSON"},
		{"null", `null`, "root"},
		{"nullDefinition", `{"nested":{"M":null}}`, "null definition"},
		{"nullFields", `{"nested":{"Empty":{"fields":{}},"M":{"fields":null}}}`, "must not be null"},
		{"unknownProperty", `{"nested":{"M":{"fields":{},"extensions":[[1,2]]}}}`, "unsupported"},
		{"edition", `{"nested":{"M":{"edition":"proto2","fields":{}}}}`, "edition"},
		{"missingType", `{"nested":{"M":{"fields":{"f":{"id":1}}}}}`, "missing type"},
		{"unresolved", `{"nested":{"p":{"nested":{"M":{"fields":{"f":{"type":"Missing","id":1}}}}}}}`, "message=p.M field=f type=Missing unresolved; tried fully-qualified names=.p.M.Missing,.p.Missing,.Missing"},
		{"numberZero", `{"nested":{"M":{"fields":{"f":{"type":"bytes","id":0}}}}}`, "field number"},
		{"reservedNumber", `{"nested":{"M":{"fields":{"f":{"type":"bytes","id":19000}}}}}`, "field number"},
		{"duplicateNumber", `{"nested":{"M":{"fields":{"f":{"type":"bytes","id":1},"g":{"type":"string","id":1}}}}}`, "registry"},
		{"required", `{"nested":{"M":{"fields":{"f":{"type":"string","id":1,"rule":"required"}}}}}`, "rule"},
		{"badMap", `{"nested":{"M":{"fields":{"f":{"type":"string","keyType":"bytes","id":1}}}}}`, "map key"},
		{"mapCollision", `{"nested":{"M":{"fields":{"f":{"type":"string","keyType":"string","id":1}},"nested":{"FEntry":{"fields":{}}}}}}`, "collision"},
		{"unknownOneof", `{"nested":{"M":{"fields":{},"oneofs":{"o":{"oneof":["absent"]}}}}}`, "invalid member"},
		{"repeatedOneof", `{"nested":{"M":{"fields":{"f":{"id":1,"type":"string","rule":"repeated"}},"oneofs":{"o":{"oneof":["f"]}}}}}`, "invalid member"},
		{"option", `{"nested":{"M":{"fields":{"f":{"id":1,"type":"string","options":{"default":"x"}}}}}}`, "unsupported"},
		{"enumNoZero", `{"nested":{"E":{"values":{"ONE":1}}}}`, "registry"},
		{"enumZeroNotFirst", `{"nested":{"E":{"values":{"ONE":1,"ZERO":0}}}}`, "registry"},
		{"unknownRPC", `{"nested":{"S":{"methods":{"m":{"requestType":"Missing","responseType":"Missing"}}}}}`, "unresolved"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build([]byte(tc.json))
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("want error containing %q; got %v", tc.contains, err)
			}
		})
	}
}

func TestProtobufJSOptionalAndAliases(t *testing.T) {
	schema := []byte(`{"nested":{"M":{"oneofs":{"_f":{"oneof":["f"]},"real":{"oneof":["s"]}},"fields":{"f":{"type":"int32","id":1,"options":{"proto3_optional":true}},"s":{"type":"string","id":2},"plain":{"type":"int32","id":3,"rule":"optional"}}},"E":{"options":{"allow_alias":true},"values":{"ZERO":0,"ALSO_ZERO":0,"ONE":1}}}}`)
	r, err := Build(schema)
	if err != nil {
		t.Fatal(err)
	}
	m, err := r.Message("M")
	if err != nil {
		t.Fatal(err)
	}
	if m.Oneofs().Get(0).Name() != "real" || !m.Oneofs().Get(1).IsSynthetic() {
		t.Fatal("synthetic oneof ordering lost")
	}
	if m.Fields().ByName("plain").HasPresence() {
		t.Fatal("protobuf.js optional rule alone must not invent proto3_optional presence")
	}
	e, err := r.Files.FindDescriptorByName("E")
	if err != nil || e.(protoreflect.EnumDescriptor).Values().Get(0).Name() != "ZERO" {
		t.Fatalf("enum declaration order changed: %v", err)
	}
	if _, err := r.Message("E"); err == nil {
		t.Fatal("enum returned as message")
	}
	if _, err := r.Message("Missing"); err == nil {
		t.Fatal("missing message accepted")
	}
}

func TestRegistryDeterministic(t *testing.T) {
	data := fixture(t, "features.json")
	a, err := Build(data)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(data)
	if err != nil {
		t.Fatal(err)
	}
	a.Files.RangeFiles(func(f protoreflect.FileDescriptor) bool {
		other, err := b.Files.FindFileByPath(f.Path())
		if err != nil || !proto.Equal(protodesc.ToFileDescriptorProto(f), protodesc.ToFileDescriptorProto(other)) {
			t.Fatalf("nondeterministic registry %s: %v", f.Path(), err)
		}
		return true
	})
}

func TestRootAndRootNamedPackage(t *testing.T) {
	r, err := Build([]byte(`{"nested":{"M":{"fields":{}},"root":{"nested":{"N":{"fields":{"m":{"id":1,"type":".M"}}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Counts.Files != 2 || r.Counts.Messages != 2 {
		t.Fatalf("root file was overwritten: %+v", r.Counts)
	}
	if _, err := r.Message("root.N"); err != nil {
		t.Fatal(err)
	}
}
