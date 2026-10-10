package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type jsonField struct {
	key   string
	value jsonValue
}

type jsonValue struct {
	kind     byte
	scalar   string
	fields   []jsonField
	elements []jsonValue
}

func canonicalReport(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	root, err := readValue(decoder, data)
	if err != nil {
		return nil, err
	}
	normalized := root.pick("unitId", "diagnostics", "members", "adviceById")
	for i := range normalized.fields {
		field := &normalized.fields[i]
		switch field.key {
		case `"diagnostics"`:
			for j := range field.value.elements {
				diagnostic := field.value.elements[j].pick("errorKey", "severity", "message", "code", "file", "range")
				for k := range diagnostic.fields {
					if diagnostic.fields[k].key == `"range"` {
						r := diagnostic.fields[k].value.pick("start", "end")
						for n := range r.fields {
							r.fields[n].value = r.fields[n].value.pick("line", "column")
						}
						diagnostic.fields[k].value = r
					}
				}
				field.value.elements[j] = diagnostic
			}
		case `"members"`:
			for j := range field.value.elements {
				field.value.elements[j] = field.value.elements[j].pick("name", "status")
			}
		}
	}
	var output bytes.Buffer
	normalized.write(&output)
	return output.Bytes(), nil
}

func (value jsonValue) pick(names ...string) jsonValue {
	result := jsonValue{kind: '{'}
	for _, name := range names {
		key := `"` + name + `"`
		for _, field := range value.fields {
			if field.key == key {
				result.fields = append(result.fields, field)
				break
			}
		}
	}
	return result
}

func readValue(decoder *json.Decoder, data []byte) (jsonValue, error) {
	start := decoder.InputOffset()
	token, err := decoder.Token()
	if err != nil {
		return jsonValue{}, err
	}
	switch token := token.(type) {
	case json.Delim:
		value := jsonValue{kind: byte(token)}
		if token == '{' {
			indices := map[string]int{}
			for decoder.More() {
				start := decoder.InputOffset()
				if _, err := decoder.Token(); err != nil {
					return jsonValue{}, err
				}
				key := canonicalString(bytes.TrimLeft(data[start:decoder.InputOffset()], " \n\r\t,:"))
				child, err := readValue(decoder, data)
				if err != nil {
					return jsonValue{}, err
				}
				if index, exists := indices[key]; exists {
					value.fields[index].value = child
				} else {
					indices[key] = len(value.fields)
					value.fields = append(value.fields, jsonField{key, child})
				}
			}
			sort.SliceStable(value.fields, func(i, j int) bool {
				a, aIndex := arrayIndex(value.fields[i].key)
				b, bIndex := arrayIndex(value.fields[j].key)
				if aIndex != bIndex {
					return aIndex
				}
				return aIndex && a < b
			})
		} else {
			for decoder.More() {
				child, err := readValue(decoder, data)
				if err != nil {
					return jsonValue{}, err
				}
				value.elements = append(value.elements, child)
			}
		}
		if _, err := decoder.Token(); err != nil {
			return jsonValue{}, err
		}
		return value, nil
	case string:
		return jsonValue{scalar: canonicalString(bytes.TrimLeft(data[start:decoder.InputOffset()], " \n\r\t,:"))}, nil
	case float64:
		return jsonValue{scalar: jsNumber(token)}, nil
	case bool:
		return jsonValue{scalar: strconv.FormatBool(token)}, nil
	default:
		return jsonValue{scalar: "null"}, nil
	}
}

func arrayIndex(key string) (uint64, bool) {
	s := key[1 : len(key)-1]
	n, err := strconv.ParseUint(s, 10, 32)
	return n, err == nil && n < 4294967295 && strconv.FormatUint(n, 10) == s
}

func (value jsonValue) write(output *bytes.Buffer) {
	switch value.kind {
	case '{':
		output.WriteByte('{')
		for i, field := range value.fields {
			if i > 0 {
				output.WriteByte(',')
			}
			output.WriteString(field.key)
			output.WriteByte(':')
			field.value.write(output)
		}
		output.WriteByte('}')
	case '[':
		output.WriteByte('[')
		for i, element := range value.elements {
			if i > 0 {
				output.WriteByte(',')
			}
			element.write(output)
		}
		output.WriteByte(']')
	default:
		output.WriteString(value.scalar)
	}
}

func jsNumber(n float64) string {
	if n == 0 {
		return "0"
	}
	absolute := n
	if absolute < 0 {
		absolute = -absolute
	}
	if absolute >= 1e-6 && absolute < 1e21 {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	s := strconv.FormatFloat(n, 'e', -1, 64)
	parts := strings.Split(s, "e")
	exponent, _ := strconv.Atoi(parts[1])
	sign := ""
	if exponent >= 0 {
		sign = "+"
	}
	return parts[0] + "e" + sign + strconv.Itoa(exponent)
}

func canonicalString(raw []byte) string {
	units := make([]uint16, 0, len(raw))
	for i := 1; i < len(raw)-1; {
		if raw[i] == '\\' {
			i++
			switch raw[i] {
			case 'u':
				n, _ := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
				units = append(units, uint16(n))
				i += 5
			case 'b':
				units = append(units, '\b')
				i++
			case 'f':
				units = append(units, '\f')
				i++
			case 'n':
				units = append(units, '\n')
				i++
			case 'r':
				units = append(units, '\r')
				i++
			case 't':
				units = append(units, '\t')
				i++
			default:
				units = append(units, uint16(raw[i]))
				i++
			}
		} else {
			r, size := utf8.DecodeRune(raw[i : len(raw)-1])
			i += size
			units = append(units, utf16.Encode([]rune{r})...)
		}
	}
	var output strings.Builder
	output.WriteByte('"')
	for i := 0; i < len(units); i++ {
		u := units[i]
		switch u {
		case '"', '\\':
			output.WriteByte('\\')
			output.WriteByte(byte(u))
		case '\b':
			output.WriteString(`\b`)
		case '\f':
			output.WriteString(`\f`)
		case '\n':
			output.WriteString(`\n`)
		case '\r':
			output.WriteString(`\r`)
		case '\t':
			output.WriteString(`\t`)
		default:
			if u < 0x20 {
				fmt.Fprintf(&output, `\u%04x`, u)
			} else if u >= 0xd800 && u <= 0xdbff && i+1 < len(units) && units[i+1] >= 0xdc00 && units[i+1] <= 0xdfff {
				output.WriteRune(utf16.DecodeRune(rune(u), rune(units[i+1])))
				i++
			} else if u >= 0xd800 && u <= 0xdfff {
				fmt.Fprintf(&output, `\u%04x`, u)
			} else {
				output.WriteRune(rune(u))
			}
		}
	}
	output.WriteByte('"')
	return output.String()
}
