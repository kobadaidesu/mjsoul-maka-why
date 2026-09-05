// Package decode implements evidence-configured envelope/name inspection and,
// since Phase 2, payload decode of confirmed messages via the exact schema.
package decode

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"mjcap/internal/capture"
	"mjcap/internal/liqi"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Profile is evidence supplied by a Phase 1 investigation, never a guessed
// built-in protocol. The repository intentionally has no real-game default.
type Profile struct {
	SchemaVersion   int    `json:"schema_version"`
	EvidenceLevel   string `json:"evidence_level"`
	EvidenceRef     string `json:"evidence_ref"`
	GameVersion     string `json:"game_version"`
	ResourceVersion string `json:"liqi_resource_version"`
	SHA256          string `json:"liqi_sha256"`
	WrapperMessage  string `json:"wrapper_message"`
	NameField       string `json:"name_field"`
	DataField       string `json:"data_field"`
	TypeOffset      int    `json:"type_offset"`
	// Notify and NotifyWrapperOffset may be explicit JSON null, declaring the
	// notify frame kind unobserved: such frames stay raw as unknown_frame_type.
	Notify              *byte  `json:"notify"`
	Request             byte   `json:"request"`
	Response            byte   `json:"response"`
	NotifyWrapperOffset *int   `json:"notify_wrapper_offset"`
	RPCWrapperOffset    int    `json:"rpc_wrapper_offset"`
	NumberOffset        int    `json:"number_offset"`
	NumberBytes         int    `json:"number_bytes"`
	ByteOrder           string `json:"byte_order"`
}

func LoadProfile(path string) (Profile, error) {
	f, err := os.Open(path)
	if err != nil {
		return Profile{}, fmt.Errorf("open protocol evidence profile: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || len(data) > 64<<10 {
		return Profile{}, fmt.Errorf("read protocol profile failed or exceeds 64 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var p Profile
	if err := d.Decode(&p); err != nil {
		return p, fmt.Errorf("parse protocol profile: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return p, fmt.Errorf("trailing protocol profile data")
	}
	// Every property must be explicit; omitted numeric properties must never
	// turn into a guessed zero-valued protocol field. Also reject duplicate keys.
	keys := map[string]json.RawMessage{}
	d = json.NewDecoder(bytes.NewReader(data))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return p, fmt.Errorf("profile must be an object")
	}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return p, err
		}
		key, ok := token.(string)
		if !ok {
			return p, fmt.Errorf("invalid profile key")
		}
		if _, exists := keys[key]; exists {
			return p, fmt.Errorf("duplicate profile key %s", key)
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return p, err
		}
		keys[key] = value
	}
	// notify may be declared unobserved with an explicit null; every other
	// property must carry a measured value.
	nullable := map[string]bool{"notify": true, "notify_wrapper_offset": true}
	for _, key := range []string{"schema_version", "evidence_level", "evidence_ref", "game_version", "liqi_resource_version", "liqi_sha256", "wrapper_message", "name_field", "data_field", "type_offset", "notify", "request", "response", "notify_wrapper_offset", "rpc_wrapper_offset", "number_offset", "number_bytes", "byte_order"} {
		value, ok := keys[key]
		if !ok || (!nullable[key] && bytes.Equal(bytes.TrimSpace(value), []byte("null"))) {
			return p, fmt.Errorf("profile requires explicit %s", key)
		}
	}
	if (p.Notify == nil) != (p.NotifyWrapperOffset == nil) {
		return p, fmt.Errorf("notify and notify_wrapper_offset must both be measured or both explicitly null")
	}
	return p, nil
}

// EquivalentProfiles reports whether two profiles bind the same envelope and
// schema evidence, ignoring where that evidence is referenced from.
func EquivalentProfiles(a, b Profile) bool {
	a.EvidenceRef, b.EvidenceRef = "", ""
	x, errX := json.Marshal(a)
	y, errY := json.Marshal(b)
	return errX == nil && errY == nil && bytes.Equal(x, y)
}

type Observation struct {
	Seq             uint64  `json:"seq"`
	ConnectionID    string  `json:"connection_id"`
	Direction       string  `json:"direction"`
	Status          string  `json:"decode_status"`
	Reason          string  `json:"reason,omitempty"`
	Kind            string  `json:"frame_kind,omitempty"`
	Number          *uint64 `json:"message_number,omitempty"`
	Name            string  `json:"message_name,omitempty"`
	ExpectedMessage string  `json:"expected_message_name,omitempty"`
}

type Names struct {
	profile   Profile
	registry  *liqi.Registry
	wrapper   protoreflect.MessageDescriptor
	nameField protoreflect.FieldDescriptor
	dataField protoreflect.FieldDescriptor
	pending   map[string]map[uint64]string
}

func (n *Names) Evidence() Profile { return n.profile }

func NewNames(p Profile, r *liqi.Resource) (*Names, error) {
	if p.SchemaVersion != 1 || p.EvidenceLevel != "CONFIRMED" || strings.TrimSpace(p.EvidenceRef) == "" {
		return nil, fmt.Errorf("name decode requires a CONFIRMED evidence profile with source references")
	}
	if r == nil || r.Registry == nil || p.GameVersion != r.Metadata.GameVersion || p.ResourceVersion != r.Metadata.ResourceVersion || p.SHA256 != r.Metadata.SHA256 {
		return nil, fmt.Errorf("protocol evidence profile does not match exact liqi version/SHA")
	}
	if p.Request == p.Response || p.TypeOffset < 0 || p.TypeOffset > 64 || p.NumberOffset < 0 || p.NumberOffset > 64 || (p.NumberBytes != 1 && p.NumberBytes != 2 && p.NumberBytes != 4) || (p.ByteOrder != "little" && p.ByteOrder != "big") || p.RPCWrapperOffset <= p.TypeOffset || p.RPCWrapperOffset < p.NumberOffset+p.NumberBytes || p.RPCWrapperOffset > 128 || (p.TypeOffset >= p.NumberOffset && p.TypeOffset < p.NumberOffset+p.NumberBytes) {
		return nil, fmt.Errorf("invalid or overlapping envelope layout in evidence profile")
	}
	if (p.Notify == nil) != (p.NotifyWrapperOffset == nil) {
		return nil, fmt.Errorf("notify evidence must measure both notify and notify_wrapper_offset or declare both null")
	}
	if p.Notify != nil && (*p.Notify == p.Request || *p.Notify == p.Response || *p.NotifyWrapperOffset <= p.TypeOffset || *p.NotifyWrapperOffset > 128) {
		return nil, fmt.Errorf("invalid or overlapping envelope layout in evidence profile")
	}
	m, err := r.Registry.Message(p.WrapperMessage)
	if err != nil {
		return nil, err
	}
	name, data := m.Fields().ByName(protoreflect.Name(p.NameField)), m.Fields().ByName(protoreflect.Name(p.DataField))
	if name == nil || data == nil || name.Kind() != protoreflect.StringKind || data.Kind() != protoreflect.BytesKind || name.IsList() || data.IsList() {
		return nil, fmt.Errorf("evidence wrapper fields do not match singular string/bytes descriptors")
	}
	return &Names{profile: p, registry: r.Registry, wrapper: m, nameField: name, dataField: data, pending: map[string]map[uint64]string{}}, nil
}

func (n *Names) Observe(e capture.Event) Observation {
	out, _ := n.observe(e)
	return out
}

// observe additionally hands back the wrapper payload bytes of a resolved
// frame so Phase 2 decode can parse them without re-reading the envelope.
func (n *Names) observe(e capture.Event) (Observation, []byte) {
	out := Observation{Seq: e.Seq, ConnectionID: e.ConnectionID, Direction: e.Direction, Status: "not_websocket"}
	if e.Kind == "websocket_close" || e.Kind == "websocket_open" {
		delete(n.pending, e.ConnectionID)
		out.Status = "connection_state_cleared"
		return out, nil
	}
	if e.Kind == "capture_start" || e.Kind == "capture_stop" {
		clear(n.pending)
		return out, nil
	}
	if e.Kind != "websocket" {
		return out, nil
	}
	if e.Opcode != 2 {
		out.Status = "non_binary_message"
		return out, nil
	}
	raw, err := hex.DecodeString(e.PayloadHex)
	if err != nil {
		out.Status = "invalid_payload_hex"
		return out, nil
	}
	p := n.profile
	if len(raw) <= p.TypeOffset {
		out.Status = "short_frame"
		return out, nil
	}
	offset := p.RPCWrapperOffset
	switch kind := raw[p.TypeOffset]; {
	case p.Notify != nil && kind == *p.Notify:
		out.Kind = "notify"
		offset = *p.NotifyWrapperOffset
	case kind == p.Request:
		out.Kind = "request"
	case kind == p.Response:
		out.Kind = "response"
	default:
		out.Status = "unknown_frame_type"
		return out, nil
	}
	if len(raw) < offset {
		out.Status = "short_frame"
		return out, nil
	}
	if out.Kind != "notify" {
		b := raw[p.NumberOffset : p.NumberOffset+p.NumberBytes]
		var number uint64
		var order binary.ByteOrder = binary.LittleEndian
		if p.ByteOrder == "big" {
			order = binary.BigEndian
		}
		switch len(b) {
		case 1:
			number = uint64(b[0])
		case 2:
			number = uint64(order.Uint16(b))
		case 4:
			number = uint64(order.Uint32(b))
		}
		out.Number = &number
	}
	w := dynamicpb.NewMessage(n.wrapper)
	if err := proto.Unmarshal(raw[offset:], w); err != nil {
		out.Status = "wrapper_decode_failed"
		out.Reason = "invalid protobuf wrapper; raw retained"
		return out, nil
	}
	name := w.Get(n.nameField).String()
	if name != "" && !safeName(name) {
		out.Status = "invalid_message_name"
		return out, nil
	}
	if out.Kind == "request" {
		if e.Direction != "sent" {
			out.Status = "unexpected_direction"
			return out, nil
		}
		if name == "" {
			out.Status = "missing_request_name"
			return out, nil
		}
		if n.pending[e.ConnectionID] == nil {
			n.pending[e.ConnectionID] = map[uint64]string{}
		}
		pending := n.pending[e.ConnectionID]
		if _, exists := pending[*out.Number]; exists {
			pending[*out.Number] = "" // Ambiguous reuse must never pick a request.
			out.Name, out.Status = "", "ambiguous_request_number"
			return out, nil
		}
		if len(pending) >= 65536 {
			out.Status = "pending_state_limit"
			return out, nil
		}
		pending[*out.Number] = name
	} else if out.Kind == "response" {
		if e.Direction != "received" {
			out.Status = "unexpected_direction"
			return out, nil
		}
		request, exists := n.pending[e.ConnectionID][*out.Number]
		delete(n.pending[e.ConnectionID], *out.Number)
		if name == "" {
			if !exists {
				out.Status = "type_unresolved"
				out.Reason = "missing observed request"
				return out, nil
			}
			if request == "" {
				out.Status = "type_unresolved"
				out.Reason = "ambiguous observed requests"
				return out, nil
			}
			name = request
		}
	}
	out.Name = name
	d, err := n.registry.Files.FindDescriptorByName(protoreflect.FullName(strings.TrimPrefix(name, ".")))
	if err != nil {
		out.Status = "unknown_message"
		return out, nil
	}
	switch d := d.(type) {
	case protoreflect.MethodDescriptor:
		if out.Kind == "request" {
			out.ExpectedMessage = "." + string(d.Input().FullName())
		} else if out.Kind == "response" {
			out.ExpectedMessage = "." + string(d.Output().FullName())
		} else {
			out.Status = "unexpected_descriptor_kind"
			return out, nil
		}
	case protoreflect.MessageDescriptor:
		if out.Kind == "request" {
			out.Status = "unexpected_descriptor_kind"
			return out, nil
		}
		out.ExpectedMessage = "." + string(d.FullName())
	default:
		out.Status = "unexpected_descriptor_kind"
		return out, nil
	}
	out.Status = "resolved"
	return out, w.Get(n.dataField).Bytes()
}

func safeName(s string) bool {
	return len(s) <= 256 && protoreflect.FullName(strings.TrimPrefix(s, ".")).IsValid()
}
