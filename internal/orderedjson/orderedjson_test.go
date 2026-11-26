package orderedjson

import "testing"

func TestDecodeRenderKeepsOrderAndLiterals(t *testing.T) {
	source := `{"z":1.50,"a":{"y":[],"x":{}},"<b>":"a&b","list":[true,null,"s"]}`
	value, err := Decode([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := Render(value)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"z\": 1.50,\n  \"a\": {\n    \"y\": [],\n    \"x\": {}\n  },\n  \"<b>\": \"a&b\",\n  \"list\": [\n    true,\n    null,\n    \"s\"\n  ]\n}\n"
	if string(rendered) != want {
		t.Fatalf("rendered:\n%s\nwant:\n%s", rendered, want)
	}
	compact, err := value.MarshalJSON()
	if err != nil || string(compact) != `{"z":1.50,"a":{"y":[],"x":{}},"<b>":"a&b","list":[true,null,"s"]}` {
		t.Fatalf("compact = %s %v", compact, err)
	}
}

func TestDecodeRefusesRepeatedKeysAndTrailingValues(t *testing.T) {
	for _, source := range []string{`{"a":1,"a":2}`, `{} {}`} {
		if _, err := Decode([]byte(source)); err == nil {
			t.Fatalf("Decode(%s) succeeded", source)
		}
	}
}

func TestEqualOrderedAndUnordered(t *testing.T) {
	left, _ := Decode([]byte(`{"a":1,"b":[1,2]}`))
	right, _ := Decode([]byte(`{"b":[1,2],"a":1}`))
	if !Equal(left, right, false) || Equal(left, right, true) {
		t.Fatal("object order handling is wrong")
	}
	swapped, _ := Decode([]byte(`{"a":1,"b":[2,1]}`))
	if Equal(left, swapped, false) {
		t.Fatal("arrays must compare in order")
	}
}
