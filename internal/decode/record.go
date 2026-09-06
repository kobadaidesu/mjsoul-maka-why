package decode

import (
	"fmt"

	"mjcap/internal/capture"
	"mjcap/internal/liqi"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Measured 2026-09-05 (docs/protocol-findings.md#phase1-live-capture): the
// observed .lq.Lobby.fetchGameRecord response carried ResGameRecord.data as a
// wrapper naming this container, whose actions list held the game events.
// These names resolve against the exact schema at runtime; nothing is decoded
// unless the supplied liqi actually defines them.
const gameDetailContainer = ".lq.GameDetailRecords"

// Decoder resolves confirmed envelope frames and decodes their payloads with
// the exact schema. It performs no domain interpretation of decoded values.
type Decoder struct {
	names    *Names
	registry *liqi.Registry
}

func NewDecoder(p Profile, r *liqi.Resource) (*Decoder, error) {
	n, err := NewNames(p, r)
	if err != nil {
		return nil, err
	}
	return &Decoder{names: n, registry: r.Registry}, nil
}

func (d *Decoder) Evidence() Profile { return d.names.Evidence() }

// DecodedFrame couples the envelope observation with the decoded payload of
// the resolved message. Message stays nil whenever decoding was not possible;
// the raw event in the private capture remains the source of truth.
type DecodedFrame struct {
	Observation
	Message *dynamicpb.Message
}

func (d *Decoder) Observe(e capture.Event) DecodedFrame {
	obs, data := d.names.observe(e)
	out := DecodedFrame{Observation: obs}
	if obs.Status != "resolved" || obs.ExpectedMessage == "" {
		return out
	}
	md, err := d.registry.Message(obs.ExpectedMessage)
	if err != nil {
		out.Status = "unknown_message"
		return out
	}
	m := dynamicpb.NewMessage(md)
	if err := proto.Unmarshal(data, m); err != nil {
		out.Status = "payload_decode_failed"
		out.Reason = "payload does not parse as expected message; raw retained"
		return out
	}
	out.Message = m
	return out
}

// ActionRecord is one GameDetailRecords action. Type and Passed are kept as
// raw numbers; their semantics are not interpreted here. Message is decoded
// only for actions whose result wrapper names a message the schema defines.
type ActionRecord struct {
	Index   int
	Passed  uint64
	Type    uint64
	Name    string
	Status  string
	Message *dynamicpb.Message
}

// GameDetail is the decoded actions view of one game record response.
type GameDetail struct {
	Version      uint64
	RecordsCount int
	Actions      []ActionRecord
}

// GameDetailActions unpacks ResGameRecord.data. Only the measured inline
// actions layout is implemented; unverified layouts fail with explicit errors
// instead of a guessed interpretation.
func (d *Decoder) GameDetailActions(res protoreflect.Message) (GameDetail, error) {
	var out GameDetail
	data, err := BytesField(res, "data")
	if err != nil {
		return out, err
	}
	if len(data) == 0 {
		url, _ := StringField(res, "data_url")
		if url != "" {
			// TODO(verify): a data_url delivery was never captured; observing the
			// browser's own HTTP fetch of it is unimplemented until measured.
			return out, fmt.Errorf("game record uses data_url delivery, which is unverified and unsupported")
		}
		return out, fmt.Errorf("game record response carries no inline data")
	}
	wrapped, err := d.unmarshalWrapper(data)
	if err != nil {
		return out, fmt.Errorf("game record data: %w", err)
	}
	container := wrapped.Get(d.names.nameField).String()
	if container != gameDetailContainer {
		return out, fmt.Errorf("game record container is %q, not the measured %s; refusing to guess its layout", container, gameDetailContainer)
	}
	md, err := d.registry.Message(gameDetailContainer)
	if err != nil {
		return out, err
	}
	detail := dynamicpb.NewMessage(md)
	if err := proto.Unmarshal(wrapped.Get(d.names.dataField).Bytes(), detail); err != nil {
		return out, fmt.Errorf("decode %s: %w", gameDetailContainer, err)
	}
	if out.Version, err = UintField(detail, "version"); err != nil {
		return out, err
	}
	records, err := ListField(detail, "records")
	if err != nil {
		return out, err
	}
	out.RecordsCount = records.Len()
	actions, err := ListField(detail, "actions")
	if err != nil {
		return out, err
	}
	if actions.Len() == 0 {
		if out.RecordsCount > 0 {
			// TODO(verify): a records-based layout has not been captured; decoding
			// it needs its own measurement instead of assuming action framing.
			return out, fmt.Errorf("game record uses the unverified records layout (%d records, no actions)", out.RecordsCount)
		}
		return out, fmt.Errorf("game record contains no actions")
	}
	for i := 0; i < actions.Len(); i++ {
		action, ok := actions.Get(i).Interface().(protoreflect.Message)
		if !ok {
			return out, fmt.Errorf("action %d is not a message", i)
		}
		record := ActionRecord{Index: i, Status: "decoded"}
		if record.Passed, err = UintField(action, "passed"); err != nil {
			return out, err
		}
		if record.Type, err = UintField(action, "type"); err != nil {
			return out, err
		}
		result, err := BytesField(action, "result")
		if err != nil {
			return out, err
		}
		if len(result) == 0 {
			record.Status = "no_result"
			out.Actions = append(out.Actions, record)
			continue
		}
		w, err := d.unmarshalWrapper(result)
		if err != nil {
			record.Status = "invalid_result_wrapper"
			out.Actions = append(out.Actions, record)
			continue
		}
		record.Name = w.Get(d.names.nameField).String()
		if record.Name == "" || !safeName(record.Name) {
			record.Status = "invalid_message_name"
			out.Actions = append(out.Actions, record)
			continue
		}
		rmd, err := d.registry.Message(record.Name)
		if err != nil {
			record.Status = "unknown_message"
			out.Actions = append(out.Actions, record)
			continue
		}
		m := dynamicpb.NewMessage(rmd)
		if err := proto.Unmarshal(w.Get(d.names.dataField).Bytes(), m); err != nil {
			record.Status = "payload_decode_failed"
			out.Actions = append(out.Actions, record)
			continue
		}
		record.Message = m
		out.Actions = append(out.Actions, record)
	}
	return out, nil
}

func (d *Decoder) unmarshalWrapper(data []byte) (*dynamicpb.Message, error) {
	w := dynamicpb.NewMessage(d.names.wrapper)
	if err := proto.Unmarshal(data, w); err != nil {
		return nil, fmt.Errorf("decode %s: %w", d.names.wrapper.FullName(), err)
	}
	return w, nil
}

func field(m protoreflect.Message, name string) (protoreflect.FieldDescriptor, error) {
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		return nil, fmt.Errorf("message %s lacks expected field %q", m.Descriptor().FullName(), name)
	}
	return fd, nil
}

func BytesField(m protoreflect.Message, name string) ([]byte, error) {
	fd, err := field(m, name)
	if err != nil {
		return nil, err
	}
	if fd.Kind() != protoreflect.BytesKind || fd.IsList() {
		return nil, fmt.Errorf("field %s.%s is not singular bytes", m.Descriptor().FullName(), name)
	}
	return m.Get(fd).Bytes(), nil
}

func StringField(m protoreflect.Message, name string) (string, error) {
	fd, err := field(m, name)
	if err != nil {
		return "", err
	}
	if fd.Kind() != protoreflect.StringKind || fd.IsList() {
		return "", fmt.Errorf("field %s.%s is not singular string", m.Descriptor().FullName(), name)
	}
	return m.Get(fd).String(), nil
}

func IntField(m protoreflect.Message, name string) (int64, error) {
	fd, err := field(m, name)
	if err != nil {
		return 0, err
	}
	switch fd.Kind() {
	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Sint32Kind, protoreflect.Sint64Kind, protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind:
		if !fd.IsList() {
			return m.Get(fd).Int(), nil
		}
	}
	return 0, fmt.Errorf("field %s.%s is not singular signed integer", m.Descriptor().FullName(), name)
}

func MessagesField(m protoreflect.Message, name string) ([]protoreflect.Message, error) {
	fd, err := field(m, name)
	if err != nil {
		return nil, err
	}
	if fd.Kind() != protoreflect.MessageKind || !fd.IsList() {
		return nil, fmt.Errorf("field %s.%s is not repeated message", m.Descriptor().FullName(), name)
	}
	list := m.Get(fd).List()
	out := make([]protoreflect.Message, list.Len())
	for i := range out {
		msg, ok := list.Get(i).Interface().(protoreflect.Message)
		if !ok {
			return nil, fmt.Errorf("field %s.%s element %d is not a message", m.Descriptor().FullName(), name, i)
		}
		out[i] = msg
	}
	return out, nil
}

// HasField reports explicit presence, which matters for message fields whose
// zero value is indistinguishable from absence via Get.
func HasField(m protoreflect.Message, name string) bool {
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	return fd != nil && m.Has(fd)
}

func MessageField(m protoreflect.Message, name string) (protoreflect.Message, error) {
	fd, err := field(m, name)
	if err != nil {
		return nil, err
	}
	if fd.Kind() != protoreflect.MessageKind || fd.IsList() || fd.IsMap() {
		return nil, fmt.Errorf("field %s.%s is not a singular message", m.Descriptor().FullName(), name)
	}
	return m.Get(fd).Message(), nil
}

func BoolField(m protoreflect.Message, name string) (bool, error) {
	fd, err := field(m, name)
	if err != nil {
		return false, err
	}
	if fd.Kind() != protoreflect.BoolKind || fd.IsList() {
		return false, fmt.Errorf("field %s.%s is not singular bool", m.Descriptor().FullName(), name)
	}
	return m.Get(fd).Bool(), nil
}

func StringsField(m protoreflect.Message, name string) ([]string, error) {
	fd, err := field(m, name)
	if err != nil {
		return nil, err
	}
	if fd.Kind() != protoreflect.StringKind || !fd.IsList() {
		return nil, fmt.Errorf("field %s.%s is not repeated string", m.Descriptor().FullName(), name)
	}
	list := m.Get(fd).List()
	out := make([]string, list.Len())
	for i := range out {
		out[i] = list.Get(i).String()
	}
	return out, nil
}

func IntsField(m protoreflect.Message, name string) ([]int64, error) {
	fd, err := field(m, name)
	if err != nil {
		return nil, err
	}
	if !fd.IsList() {
		return nil, fmt.Errorf("field %s.%s is not repeated", m.Descriptor().FullName(), name)
	}
	list := m.Get(fd).List()
	out := make([]int64, list.Len())
	for i := range out {
		switch fd.Kind() {
		case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Sint32Kind, protoreflect.Sint64Kind, protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind:
			out[i] = list.Get(i).Int()
		case protoreflect.Uint32Kind, protoreflect.Uint64Kind, protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
			out[i] = int64(list.Get(i).Uint())
		default:
			return nil, fmt.Errorf("field %s.%s is not repeated integer", m.Descriptor().FullName(), name)
		}
	}
	return out, nil
}

func UintField(m protoreflect.Message, name string) (uint64, error) {
	fd, err := field(m, name)
	if err != nil {
		return 0, err
	}
	switch fd.Kind() {
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind, protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
		if !fd.IsList() {
			return m.Get(fd).Uint(), nil
		}
	}
	return 0, fmt.Errorf("field %s.%s is not singular unsigned", m.Descriptor().FullName(), name)
}

func ListField(m protoreflect.Message, name string) (protoreflect.List, error) {
	fd, err := field(m, name)
	if err != nil {
		return nil, err
	}
	if !fd.IsList() {
		return nil, fmt.Errorf("field %s.%s is not repeated", m.Descriptor().FullName(), name)
	}
	return m.Get(fd).List(), nil
}
