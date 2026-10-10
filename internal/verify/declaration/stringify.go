package declaration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

func stringify(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	result, err := stringifyValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing manifest JSON")
	}
	return result, nil
}

type property struct {
	key   string
	value []byte
}

func stringifyValue(decoder *json.Decoder) ([]byte, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			properties := []property{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				name, ok := key.(string)
				if !ok {
					return nil, fmt.Errorf("expected JSON property")
				}
				encoded, err := stringifyValue(decoder)
				if err != nil {
					return nil, err
				}
				replaced := false
				for i := range properties {
					if properties[i].key == name {
						properties[i].value = encoded
						replaced = true
						break
					}
				}
				if !replaced {
					properties = append(properties, property{name, encoded})
				}
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			sort.SliceStable(properties, func(i, j int) bool {
				a, ai := arrayIndex(properties[i].key)
				b, bi := arrayIndex(properties[j].key)
				if ai != bi {
					return ai
				}
				return ai && a < b
			})
			var out bytes.Buffer
			out.WriteByte('{')
			for i, p := range properties {
				if i > 0 {
					out.WriteByte(',')
				}
				out.Write(jsonString(p.key))
				out.WriteByte(':')
				out.Write(p.value)
			}
			out.WriteByte('}')
			return out.Bytes(), nil
		case '[':
			var out bytes.Buffer
			out.WriteByte('[')
			for i := 0; decoder.More(); i++ {
				if i > 0 {
					out.WriteByte(',')
				}
				encoded, err := stringifyValue(decoder)
				if err != nil {
					return nil, err
				}
				out.Write(encoded)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			out.WriteByte(']')
			return out.Bytes(), nil
		}
	case string:
		return jsonString(value), nil
	case json.Number:
		number, err := value.Float64()
		if err != nil {
			return nil, err
		}
		if number == 0 {
			return []byte("0"), nil
		}
		format := byte('f')
		if math.Abs(number) >= 1e21 || math.Abs(number) < 1e-6 {
			format = 'e'
		}
		encoded := strconv.FormatFloat(number, format, -1, 64)
		encoded = strings.ReplaceAll(strings.ReplaceAll(encoded, "e-0", "e-"), "e+0", "e+")
		return []byte(encoded), nil
	case bool:
		return []byte(strconv.FormatBool(value)), nil
	case nil:
		return []byte("null"), nil
	}
	return nil, fmt.Errorf("unexpected JSON token %v", token)
}

func arrayIndex(key string) (uint64, bool) {
	number, err := strconv.ParseUint(key, 10, 32)
	return number, err == nil && number < 4294967295 && strconv.FormatUint(number, 10) == key
}

func jsonString(value string) []byte {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	data := bytes.TrimSuffix(out.Bytes(), []byte("\n"))
	var result bytes.Buffer
	for i := 0; i < len(data); i++ {
		if data[i] == '\\' && i+1 < len(data) {
			if i+6 <= len(data) && (string(data[i:i+6]) == `\u2028` || string(data[i:i+6]) == `\u2029`) {
				if data[i+5] == '8' {
					result.WriteString("\u2028")
				} else {
					result.WriteString("\u2029")
				}
				i += 5
			} else {
				result.Write(data[i : i+2])
				i++
			}
		} else {
			result.WriteByte(data[i])
		}
	}
	return result.Bytes()
}
