// Package orderedjson decodes and renders JSON while keeping object member
// order. Contract 0.14 passes authored Pkl mappings (compilerOptions, package
// fields) into generated files in their declared order, which a Go map would
// lose.
package orderedjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Kind classifies a Value.
type Kind uint8

const (
	Null Kind = iota
	Bool
	Number
	String
	Array
	Object
)

// Value is one JSON value. Objects keep member order; scalars keep their
// literal text (numbers are never reformatted).
type Value struct {
	Kind    Kind
	Literal string
	Items   []Value
	Members []Member
}

// Member is one object member.
type Member struct {
	Key   string
	Value Value
}

// StringValue returns a JSON string.
func StringValue(text string) Value { return Value{Kind: String, Literal: text} }

// BoolValue returns a JSON boolean.
func BoolValue(value bool) Value {
	if value {
		return Value{Kind: Bool, Literal: "true"}
	}
	return Value{Kind: Bool, Literal: "false"}
}

// ArrayValue returns a JSON array; a nil items slice renders as [].
func ArrayValue(items ...Value) Value { return Value{Kind: Array, Items: append([]Value{}, items...)} }

// StringArray returns an array of strings.
func StringArray(values []string) Value {
	items := make([]Value, 0, len(values))
	for _, value := range values {
		items = append(items, StringValue(value))
	}
	return Value{Kind: Array, Items: items}
}

// ObjectValue returns an empty object.
func ObjectValue() Value { return Value{Kind: Object, Members: []Member{}} }

// Set replaces the member named key, or appends it when absent.
func (value *Value) Set(key string, member Value) {
	for index := range value.Members {
		if value.Members[index].Key == key {
			value.Members[index].Value = member
			return
		}
	}
	value.Members = append(value.Members, Member{Key: key, Value: member})
}

// Get returns the member named key.
func (value Value) Get(key string) (Value, bool) {
	for _, member := range value.Members {
		if member.Key == key {
			return member.Value, true
		}
	}
	return Value{}, false
}

// Keys returns member keys in order.
func (value Value) Keys() []string {
	keys := make([]string, 0, len(value.Members))
	for _, member := range value.Members {
		keys = append(keys, member.Key)
	}
	return keys
}

// Text returns the decoded string of a String value.
func (value Value) Text() string { return value.Literal }

// IsTrue reports whether value is the boolean true.
func (value Value) IsTrue() bool { return value.Kind == Bool && value.Literal == "true" }

// UnmarshalJSON decodes one JSON value, keeping member order.
func (value *Value) UnmarshalJSON(encoded []byte) error {
	decoded, err := Decode(encoded)
	if err != nil {
		return err
	}
	*value = decoded
	return nil
}

// MarshalJSON renders the value compactly.
func (value Value) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	if err := value.write(&buffer, "", ""); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// Decode parses exactly one JSON value. Duplicate object keys are refused.
func Decode(encoded []byte) (Value, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	value, err := decodeValue(decoder)
	if err != nil {
		return Value{}, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Value{}, fmt.Errorf("trailing JSON value")
	}
	return value, nil
}

func decodeValue(decoder *json.Decoder) (Value, error) {
	token, err := decoder.Token()
	if err != nil {
		return Value{}, err
	}
	switch typed := token.(type) {
	case nil:
		return Value{Kind: Null, Literal: "null"}, nil
	case bool:
		return BoolValue(typed), nil
	case json.Number:
		return Value{Kind: Number, Literal: typed.String()}, nil
	case string:
		return StringValue(typed), nil
	case json.Delim:
		switch typed {
		case '[':
			items := make([]Value, 0)
			for decoder.More() {
				item, err := decodeValue(decoder)
				if err != nil {
					return Value{}, err
				}
				items = append(items, item)
			}
			if _, err := decoder.Token(); err != nil {
				return Value{}, err
			}
			return Value{Kind: Array, Items: items}, nil
		case '{':
			members := make([]Member, 0)
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return Value{}, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return Value{}, fmt.Errorf("object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return Value{}, fmt.Errorf("object key %q is repeated", key)
				}
				seen[key] = struct{}{}
				member, err := decodeValue(decoder)
				if err != nil {
					return Value{}, err
				}
				members = append(members, Member{Key: key, Value: member})
			}
			if _, err := decoder.Token(); err != nil {
				return Value{}, err
			}
			return Value{Kind: Object, Members: members}, nil
		}
	}
	return Value{}, fmt.Errorf("unexpected JSON token %v", token)
}

// Render renders a generated file: two-space indentation, LF line endings, no
// HTML escaping and a trailing newline. Empty arrays and objects render as []
// and {}.
func Render(value Value) ([]byte, error) {
	var buffer bytes.Buffer
	if err := value.write(&buffer, "\n", "  "); err != nil {
		return nil, err
	}
	buffer.WriteByte('\n')
	return buffer.Bytes(), nil
}

func (value Value) write(buffer *bytes.Buffer, prefix, indent string) error {
	switch value.Kind {
	case Null:
		buffer.WriteString("null")
	case Bool, Number:
		buffer.WriteString(value.Literal)
	case String:
		writeString(buffer, value.Literal)
	case Array:
		if len(value.Items) == 0 {
			buffer.WriteString("[]")
			return nil
		}
		buffer.WriteByte('[')
		inner := prefix + indent
		for index, item := range value.Items {
			if index > 0 {
				buffer.WriteByte(',')
			}
			buffer.WriteString(inner)
			if err := item.write(buffer, inner, indent); err != nil {
				return err
			}
		}
		buffer.WriteString(prefix)
		buffer.WriteByte(']')
	case Object:
		if len(value.Members) == 0 {
			buffer.WriteString("{}")
			return nil
		}
		buffer.WriteByte('{')
		inner := prefix + indent
		for index, member := range value.Members {
			if index > 0 {
				buffer.WriteByte(',')
			}
			buffer.WriteString(inner)
			writeString(buffer, member.Key)
			buffer.WriteByte(':')
			if indent != "" {
				buffer.WriteByte(' ')
			}
			if err := member.Value.write(buffer, inner, indent); err != nil {
				return err
			}
		}
		buffer.WriteString(prefix)
		buffer.WriteByte('}')
	default:
		return fmt.Errorf("unknown JSON value kind %d", value.Kind)
	}
	return nil
}

func writeString(buffer *bytes.Buffer, text string) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(text)
	buffer.WriteString(strings.TrimSuffix(encoded.String(), "\n"))
}

// Equal reports structural equality. Object member order is significant when
// ordered is true; arrays are always compared in order.
func Equal(left, right Value, ordered bool) bool {
	if left.Kind != right.Kind {
		return false
	}
	switch left.Kind {
	case Array:
		if len(left.Items) != len(right.Items) {
			return false
		}
		for index := range left.Items {
			if !Equal(left.Items[index], right.Items[index], ordered) {
				return false
			}
		}
		return true
	case Object:
		if len(left.Members) != len(right.Members) {
			return false
		}
		for index, member := range left.Members {
			if ordered {
				if right.Members[index].Key != member.Key || !Equal(member.Value, right.Members[index].Value, ordered) {
					return false
				}
				continue
			}
			other, exists := right.Get(member.Key)
			if !exists || !Equal(member.Value, other, ordered) {
				return false
			}
		}
		return true
	case Number:
		return left.Literal == right.Literal
	default:
		return left.Literal == right.Literal
	}
}
