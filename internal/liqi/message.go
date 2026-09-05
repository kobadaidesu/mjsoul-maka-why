package liqi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func (b *builder) message(n *node, name, scope, file string) (*descriptorpb.DescriptorProto, error) {
	if err := checkOptions(n.Options, "deprecated"); err != nil {
		return nil, fmt.Errorf("message %s: %w", scope, err)
	}
	deprecated, err := boolOption(n.Options, "deprecated")
	if err != nil {
		return nil, err
	}
	m := &descriptorpb.DescriptorProto{Name: proto.String(name), Options: &descriptorpb.MessageOptions{Deprecated: deprecated}}
	for _, childName := range keys(n.Nested) {
		child := n.Nested[childName]
		if child.Fields != nil {
			nested, err := b.message(child, childName, full(scope, childName), file)
			if err != nil {
				return nil, err
			}
			m.NestedType = append(m.NestedType, nested)
		} else {
			e, err := enum(child, childName)
			if err != nil {
				return nil, fmt.Errorf("enum %s.%s: %w", scope, childName, err)
			}
			m.EnumType = append(m.EnumType, e)
		}
	}
	member := map[string]int32{}
	synthetic := map[string]bool{}
	usedNames := map[string]bool{}
	for k := range n.Fields {
		usedNames[k] = true
	}
	for k := range n.Nested {
		usedNames[k] = true
	}
	// Real oneofs must precede protobuf.js proto3_optional synthetic oneofs.
	for _, wantSynthetic := range []bool{false, true} {
		for _, key := range keys(n.Oneofs) {
			o := n.Oneofs[key]
			if err := checkOptions(o.Options); err != nil {
				return nil, fmt.Errorf("message %s oneof %s: %w", scope, key, err)
			}
			if len(o.Oneof) == 0 {
				return nil, fmt.Errorf("message %s oneof %s is empty", scope, key)
			}
			isSynthetic := false
			for _, f := range o.Oneof {
				v, err := boolOption(n.Fields[f].Options, "proto3_optional")
				if err != nil {
					return nil, err
				}
				isSynthetic = isSynthetic || (v != nil && *v)
			}
			if isSynthetic && len(o.Oneof) != 1 {
				return nil, fmt.Errorf("message %s oneof %s: proto3_optional requires one member", scope, key)
			}
			if isSynthetic != wantSynthetic {
				continue
			}
			if usedNames[key] {
				return nil, fmt.Errorf("message %s: oneof %s name collision", scope, key)
			}
			usedNames[key] = true
			for _, fieldName := range o.Oneof {
				f, exists := n.Fields[fieldName]
				_, duplicate := member[fieldName]
				if !exists || duplicate || f.Rule == "repeated" || f.KeyType != "" {
					return nil, fmt.Errorf("message %s oneof %s: invalid member %s", scope, key, fieldName)
				}
				member[fieldName] = int32(len(m.OneofDecl))
				synthetic[fieldName] = isSynthetic
			}
			m.OneofDecl = append(m.OneofDecl, &descriptorpb.OneofDescriptorProto{Name: proto.String(key)})
		}
	}
	for _, fieldName := range keys(n.Fields) {
		f := n.Fields[fieldName]
		fd, err := b.field(f, fieldName, scope, file)
		if err != nil {
			return nil, fmt.Errorf("message %s field %s type %s: %w", scope, fieldName, f.Type, err)
		}
		if idx, ok := member[fieldName]; ok {
			fd.OneofIndex = proto.Int32(idx)
			if synthetic[fieldName] {
				fd.Proto3Optional = proto.Bool(true)
			}
		} else if fd.GetProto3Optional() {
			oneofName := "_" + fieldName
			for usedNames[oneofName] {
				oneofName = "_" + oneofName
			}
			usedNames[oneofName] = true
			fd.OneofIndex = proto.Int32(int32(len(m.OneofDecl)))
			fd.Proto3Optional = proto.Bool(true)
			m.OneofDecl = append(m.OneofDecl, &descriptorpb.OneofDescriptorProto{Name: &oneofName})
		}
		if f.KeyType != "" {
			if f.Rule != "" || fd.OneofIndex != nil || fd.GetProto3Optional() {
				return nil, fmt.Errorf("message %s field %s: map cannot have rule/oneof/optional", scope, fieldName)
			}
			keyKind, ok := scalarTypes[f.KeyType]
			if !ok || f.KeyType == "bytes" || f.KeyType == "float" || f.KeyType == "double" {
				return nil, fmt.Errorf("message %s field %s: invalid map key type %s", scope, fieldName, f.KeyType)
			}
			entryName := mapEntryName(fieldName)
			if usedNames[entryName] {
				return nil, fmt.Errorf("message %s field %s: map entry name collision %s", scope, fieldName, entryName)
			}
			usedNames[entryName] = true
			// Map entry field numbers 1/2 are defined by the protobuf map format.
			entry := &descriptorpb.DescriptorProto{Name: &entryName, Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)}, Field: []*descriptorpb.FieldDescriptorProto{
				{Name: proto.String("key"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: keyKind.Enum()},
				{Name: proto.String("value"), Number: proto.Int32(2), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: fd.Type, TypeName: fd.TypeName},
			}}
			m.NestedType = append(m.NestedType, entry)
			fd.Type = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
			fd.TypeName = proto.String("." + full(scope, entryName))
			fd.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
		}
		m.Field = append(m.Field, fd)
	}
	// Descriptor format requires each oneof's members to be consecutive.
	// JSON object key order is not semantic; field numbers remain untouched.
	sort.SliceStable(m.Field, func(i, j int) bool {
		a, z := m.Field[i], m.Field[j]
		if a.OneofIndex == nil || z.OneofIndex == nil {
			return a.OneofIndex == nil && z.OneofIndex != nil
		}
		return a.GetOneofIndex() < z.GetOneofIndex()
	})
	return m, nil
}

func (b *builder) field(f field, name, scope, file string) (*descriptorpb.FieldDescriptorProto, error) {
	if f.ID <= 0 || f.ID > (1<<29)-1 || (f.ID >= 19000 && f.ID <= 19999) || f.Type == "" {
		return nil, fmt.Errorf("invalid field number or missing type")
	}
	if err := checkOptions(f.Options, "packed", "deprecated", "proto3_optional", "json_name"); err != nil {
		return nil, err
	}
	label := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	switch f.Rule {
	case "", "optional":
	case "repeated":
		label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	default:
		return nil, fmt.Errorf("unsupported proto3 field rule %q", f.Rule)
	}
	typ, fq, err := b.resolve(f.Type, scope, name, file)
	if err != nil {
		return nil, err
	}
	fd := &descriptorpb.FieldDescriptorProto{Name: &name, Number: &f.ID, Label: &label, Type: &typ}
	if fq != "" {
		fd.TypeName = &fq
	}
	packed, err := boolOption(f.Options, "packed")
	if err != nil {
		return nil, err
	}
	deprecated, err := boolOption(f.Options, "deprecated")
	if err != nil {
		return nil, err
	}
	fd.Proto3Optional, err = boolOption(f.Options, "proto3_optional")
	if err != nil {
		return nil, err
	}
	if fd.GetProto3Optional() && label == descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
		return nil, fmt.Errorf("repeated field cannot be proto3_optional")
	}
	fd.Options = &descriptorpb.FieldOptions{Packed: packed, Deprecated: deprecated}
	if raw, ok := f.Options["json_name"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil || s == "" {
			return nil, fmt.Errorf("invalid json_name")
		}
		fd.JsonName = &s
	}
	return fd, nil
}

func mapEntryName(s string) string {
	var out strings.Builder
	upper := true
	for _, c := range s {
		if c == '_' {
			upper = true
			continue
		}
		if upper && c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		out.WriteRune(c)
		upper = false
	}
	return out.String() + "Entry"
}
