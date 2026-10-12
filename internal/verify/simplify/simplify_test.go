package simplify_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/phosphorco/workbench-go/internal/verify/simplify"
)

func TestSimplifierParity(t *testing.T) {
	data, err := os.ReadFile("testdata/parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name     string
		Input    simplify.Input[json.RawMessage]
		Expected string
	}
	if err = json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			var got bytes.Buffer
			encoder := json.NewEncoder(&got)
			encoder.SetEscapeHTML(false)
			if err := encoder.Encode(simplify.Simplify(fixture.Input)); err != nil {
				t.Fatal(err)
			}
			if string(bytes.TrimSuffix(got.Bytes(), []byte("\n"))) != fixture.Expected {
				t.Fatalf("Go structure differs from TypeScript:\n%s\nwant:\n%s", got.String(), fixture.Expected)
			}
		})
	}
}
