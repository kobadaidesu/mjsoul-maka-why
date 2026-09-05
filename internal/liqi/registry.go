package liqi

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

type Counts struct {
	Files    int
	Messages int // Includes nested messages and synthetic map entries.
	Enums    int
}

type Registry struct {
	Files  *protoregistry.Files
	Counts Counts
}

func (r *Registry) Message(name string) (protoreflect.MessageDescriptor, error) {
	d, err := r.Files.FindDescriptorByName(protoreflect.FullName(strings.TrimPrefix(name, ".")))
	if err != nil {
		return nil, fmt.Errorf("find message %s: %w", name, err)
	}
	m, ok := d.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, fmt.Errorf("descriptor %s is not a message", name)
	}
	return m, nil
}

type symbol struct {
	kind descriptorpb.FieldDescriptorProto_Type
	file string
}

type builder struct {
	symbols map[string]symbol
	files   map[string]*descriptorpb.FileDescriptorProto
	deps    map[string]map[string]bool
}

// Build converts a protobuf.js JSON document directly, without generated code.
// The implicit syntax is proto3, as in protobuf.js Type.fromJSON. Unsupported
// schema properties fail explicitly; unresolved types are never placeholders.
func Build(data []byte) (*Registry, error) {
	root, err := parse(data)
	if err != nil {
		return nil, err
	}
	b := &builder{symbols: map[string]symbol{}, files: map[string]*descriptorpb.FileDescriptorProto{}, deps: map[string]map[string]bool{}}
	if err := b.collect(root, "", "", false); err != nil {
		return nil, err
	}
	if err := b.namespace(root, ""); err != nil {
		return nil, err
	}
	set := &descriptorpb.FileDescriptorSet{}
	for _, path := range keys(b.files) {
		f := b.files[path]
		f.Dependency = keys(b.deps[path])
		set.File = append(set.File, f)
	}
	if len(set.File) == 0 {
		return nil, fmt.Errorf("schema contains no descriptors")
	}
	files, err := protodesc.NewFiles(set)
	if err != nil {
		return nil, fmt.Errorf("validate descriptor registry: %w", err)
	}
	r := &Registry{Files: files}
	var count func(protoreflect.MessageDescriptors)
	count = func(ms protoreflect.MessageDescriptors) {
		for i := 0; i < ms.Len(); i++ {
			m := ms.Get(i)
			r.Counts.Messages++
			r.Counts.Enums += m.Enums().Len()
			count(m.Messages())
		}
	}
	files.RangeFiles(func(f protoreflect.FileDescriptor) bool {
		r.Counts.Files++
		r.Counts.Enums += f.Enums().Len()
		count(f.Messages())
		return true
	})
	return r, nil
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func full(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

func filePath(pkg string) string {
	if pkg == "" {
		return "liqi/root.proto"
	}
	return "liqi/packages/" + strings.ReplaceAll(pkg, ".", "/") + "/schema.proto"
}

func (b *builder) collect(n *node, scope, pkg string, insideMessage bool) error {
	if n == nil {
		return fmt.Errorf("null definition %s", scope)
	}
	if n.Edition != "" && n.Edition != "proto3" {
		return fmt.Errorf("definition %s: unsupported edition %q", scope, n.Edition)
	}
	kinds := 0
	for _, present := range []bool{n.Fields != nil, n.Values != nil, n.Methods != nil} {
		if present {
			kinds++
		}
	}
	if kinds > 1 || (n.Oneofs != nil && n.Fields == nil) {
		return fmt.Errorf("ambiguous definition %s", scope)
	}
	if n.Fields != nil || n.Values != nil {
		kind := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
		if n.Values != nil {
			kind = descriptorpb.FieldDescriptorProto_TYPE_ENUM
		}
		b.symbols[scope] = symbol{kind: kind, file: filePath(pkg)}
	} else if insideMessage {
		return fmt.Errorf("unsupported namespace/service nested in message %s", scope)
	}
	if (n.Values != nil || n.Methods != nil) && len(n.Nested) > 0 {
		return fmt.Errorf("unsupported nested definitions in %s", scope)
	}
	for _, name := range keys(n.Nested) {
		if !protoreflect.Name(name).IsValid() {
			return fmt.Errorf("invalid definition name in %s", scope)
		}
		child := n.Nested[name]
		childPkg := pkg
		if n.Fields == nil && n.Values == nil && n.Methods == nil {
			childPkg = scope
		}
		if err := b.collect(child, full(scope, name), childPkg, n.Fields != nil); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) namespace(n *node, pkg string) error {
	if err := checkOptions(n.Options, "go_package"); err != nil {
		return fmt.Errorf("namespace %s: %w", pkg, err)
	}
	f := &descriptorpb.FileDescriptorProto{Name: proto.String(filePath(pkg)), Package: proto.String(pkg), Syntax: proto.String("proto3")}
	if raw, ok := n.Options["go_package"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil || s == "" {
			return fmt.Errorf("namespace %s: invalid go_package", pkg)
		}
		f.Options = &descriptorpb.FileOptions{GoPackage: &s}
	}
	b.deps[f.GetName()] = map[string]bool{}
	for _, name := range keys(n.Nested) {
		child := n.Nested[name]
		scope := full(pkg, name)
		switch {
		case child.Fields != nil:
			m, err := b.message(child, name, scope, f.GetName())
			if err != nil {
				return err
			}
			f.MessageType = append(f.MessageType, m)
		case child.Values != nil:
			e, err := enum(child, name)
			if err != nil {
				return fmt.Errorf("enum %s: %w", scope, err)
			}
			f.EnumType = append(f.EnumType, e)
		case child.Methods != nil:
			s, err := b.service(child, name, scope, f.GetName())
			if err != nil {
				return err
			}
			f.Service = append(f.Service, s)
		default:
			if err := b.namespace(child, scope); err != nil {
				return err
			}
		}
	}
	if len(f.MessageType)+len(f.EnumType)+len(f.Service) > 0 {
		b.files[f.GetName()] = f
	}
	return nil
}

func enum(n *node, name string) (*descriptorpb.EnumDescriptorProto, error) {
	if err := checkOptions(n.Options, "allow_alias", "deprecated"); err != nil {
		return nil, err
	}
	alias, err := boolOption(n.Options, "allow_alias")
	if err != nil {
		return nil, err
	}
	deprecated, err := boolOption(n.Options, "deprecated")
	if err != nil {
		return nil, err
	}
	e := &descriptorpb.EnumDescriptorProto{Name: proto.String(name), Options: &descriptorpb.EnumOptions{AllowAlias: alias, Deprecated: deprecated}}
	// Preserve declaration order: protobuf.js uses the first enum value as the
	// default, and the first alias determines its canonical JSON name.
	for _, k := range n.enumOrder {
		e.Value = append(e.Value, &descriptorpb.EnumValueDescriptorProto{Name: proto.String(k), Number: proto.Int32(n.Values[k])})
	}
	return e, nil
}

var scalarTypes = map[string]descriptorpb.FieldDescriptorProto_Type{
	"double": descriptorpb.FieldDescriptorProto_TYPE_DOUBLE, "float": descriptorpb.FieldDescriptorProto_TYPE_FLOAT,
	"int64": descriptorpb.FieldDescriptorProto_TYPE_INT64, "uint64": descriptorpb.FieldDescriptorProto_TYPE_UINT64,
	"int32": descriptorpb.FieldDescriptorProto_TYPE_INT32, "fixed64": descriptorpb.FieldDescriptorProto_TYPE_FIXED64,
	"fixed32": descriptorpb.FieldDescriptorProto_TYPE_FIXED32, "bool": descriptorpb.FieldDescriptorProto_TYPE_BOOL,
	"string": descriptorpb.FieldDescriptorProto_TYPE_STRING, "bytes": descriptorpb.FieldDescriptorProto_TYPE_BYTES,
	"uint32": descriptorpb.FieldDescriptorProto_TYPE_UINT32, "sfixed32": descriptorpb.FieldDescriptorProto_TYPE_SFIXED32,
	"sfixed64": descriptorpb.FieldDescriptorProto_TYPE_SFIXED64, "sint32": descriptorpb.FieldDescriptorProto_TYPE_SINT32,
	"sint64": descriptorpb.FieldDescriptorProto_TYPE_SINT64,
}

func (b *builder) resolve(typ, scope, fieldName, file string) (descriptorpb.FieldDescriptorProto_Type, string, error) {
	if scalar, ok := scalarTypes[typ]; ok {
		return scalar, "", nil
	}
	var tried []string
	if strings.HasPrefix(typ, ".") {
		tried = append(tried, typ)
	} else {
		for parent := scope; ; {
			tried = append(tried, "."+full(parent, typ))
			if parent == "" {
				break
			}
			idx := strings.LastIndexByte(parent, '.')
			if idx < 0 {
				parent = ""
			} else {
				parent = parent[:idx]
			}
		}
	}
	for _, candidate := range tried {
		if sym, ok := b.symbols[strings.TrimPrefix(candidate, ".")]; ok {
			if sym.file != file {
				b.deps[file][sym.file] = true
			}
			return sym.kind, candidate, nil
		}
	}
	return 0, "", fmt.Errorf("message=%s field=%s type=%s unresolved; tried fully-qualified names=%s", scope, fieldName, typ, strings.Join(tried, ","))
}

func (b *builder) service(n *node, name, scope, file string) (*descriptorpb.ServiceDescriptorProto, error) {
	if err := checkOptions(n.Options); err != nil {
		return nil, fmt.Errorf("service %s: %w", scope, err)
	}
	s := &descriptorpb.ServiceDescriptorProto{Name: proto.String(name)}
	for _, methodName := range keys(n.Methods) {
		m := n.Methods[methodName]
		inKind, in, err := b.resolve(m.RequestType, scope, methodName+".request", file)
		if err != nil {
			return nil, err
		}
		outKind, out, err := b.resolve(m.ResponseType, scope, methodName+".response", file)
		if err != nil {
			return nil, err
		}
		if inKind != descriptorpb.FieldDescriptorProto_TYPE_MESSAGE || outKind != descriptorpb.FieldDescriptorProto_TYPE_MESSAGE {
			return nil, fmt.Errorf("service %s method %s requires message input/output", scope, methodName)
		}
		s.Method = append(s.Method, &descriptorpb.MethodDescriptorProto{Name: proto.String(methodName), InputType: &in, OutputType: &out, ClientStreaming: &m.RequestStream, ServerStreaming: &m.ResponseStream})
	}
	return s, nil
}
