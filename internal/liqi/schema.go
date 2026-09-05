// Package liqi resolves public schema resources and builds runtime descriptors.
package liqi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const maxSchemaBytes = 8 << 20

// These are protobuf.js JSON schema keys, not Mahjong Soul message fields.
type node struct {
	Nested    map[string]*node           `json:"nested"`
	Fields    map[string]field           `json:"fields"`
	Values    map[string]int32           `json:"values"`
	Oneofs    map[string]oneof           `json:"oneofs"`
	Methods   map[string]method          `json:"methods"`
	Options   map[string]json.RawMessage `json:"options"`
	Comment   string                     `json:"comment"`
	Edition   string                     `json:"edition"`
	enumOrder []string
}

func (n *node) UnmarshalJSON(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return fmt.Errorf("schema root/definition must be an object")
	}
	type plain node
	var v plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, key := range []string{"nested", "fields", "values", "oneofs", "methods"} {
		if value, ok := raw[key]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("definition property %s must not be null", key)
		}
	}
	if value, ok := raw["values"]; ok {
		d := json.NewDecoder(bytes.NewReader(value))
		if _, err := d.Token(); err != nil {
			return err
		}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			var number int32
			if err := d.Decode(&number); err != nil {
				return err
			}
			v.enumOrder = append(v.enumOrder, key.(string))
		}
	}
	*n = node(v)
	return nil
}

type field struct {
	Type    string                     `json:"type"`
	ID      int32                      `json:"id"`
	Rule    string                     `json:"rule"`
	KeyType string                     `json:"keyType"`
	Options map[string]json.RawMessage `json:"options"`
	Comment string                     `json:"comment"`
}

type oneof struct {
	Oneof   []string                   `json:"oneof"`
	Options map[string]json.RawMessage `json:"options"`
	Comment string                     `json:"comment"`
}

type method struct {
	RequestType    string `json:"requestType"`
	ResponseType   string `json:"responseType"`
	RequestStream  bool   `json:"requestStream"`
	ResponseStream bool   `json:"responseStream"`
	Comment        string `json:"comment"`
}

func parse(data []byte) (*node, error) {
	if len(data) == 0 || len(data) > maxSchemaBytes {
		return nil, fmt.Errorf("liqi JSON size must be 1..%d bytes", maxSchemaBytes)
	}
	if err := validateJSON(data); err != nil {
		return nil, fmt.Errorf("liqi JSON: %w", err)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var root node
	if err := d.Decode(&root); err != nil {
		return nil, fmt.Errorf("parse protobuf.js schema (unsupported properties are rejected): %w", err)
	}
	if root.Nested == nil || root.Fields != nil || root.Values != nil || root.Methods != nil {
		return nil, fmt.Errorf("liqi JSON root must be a namespace with nested definitions")
	}
	return &root, nil
}

// Reject duplicate keys, trailing documents, and excessive nesting before any
// typed decode: encoding/json otherwise silently replaces duplicate keys.
func validateJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := jsonValue(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("expected end of JSON")
	}
	return nil
}

func jsonValue(d *json.Decoder, depth int) error {
	if depth > 128 {
		return fmt.Errorf("JSON nesting exceeds 128")
	}
	t, err := d.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return fmt.Errorf("unexpected JSON delimiter")
	}
	seen := make(map[string]bool)
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return fmt.Errorf("invalid or duplicate JSON key")
			}
			seen[s] = true
		}
		if err := jsonValue(d, depth+1); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func checkOptions(options map[string]json.RawMessage, allowed ...string) error {
	for key := range options {
		found := false
		for _, a := range allowed {
			found = found || key == a
		}
		if !found {
			return fmt.Errorf("unsupported protobuf.js option %q", key)
		}
	}
	return nil
}

func boolOption(options map[string]json.RawMessage, name string) (*bool, error) {
	raw, ok := options[name]
	if !ok {
		return nil, nil
	}
	var v bool
	if bytes.Equal(raw, []byte("null")) {
		return nil, fmt.Errorf("option %s must be boolean", name)
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("option %s must be boolean", name)
	}
	return &v, nil
}
