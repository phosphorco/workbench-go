package report_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/phosphorco/workbench-go/internal/verify/report"
)

func TestCanonicalReportGolden(t *testing.T) {
	data, err := os.ReadFile("testdata/serialization.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct{ Name, Input, Canonical, Digest string }
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	canonicalByName := map[string][]byte{}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			decoded := report.Decode([]byte(fixture.Input), visibility())
			if decoded.Report == nil {
				t.Fatal(decoded.Refused)
			}
			if !bytes.Equal(decoded.Bytes, []byte(fixture.Canonical)) {
				t.Fatalf("canonical bytes differ\ngot:  %s\nwant: %s", decoded.Bytes, fixture.Canonical)
			}
			hash := sha256.Sum256(decoded.Bytes)
			if hex.EncodeToString(hash[:]) != fixture.Digest {
				t.Fatal("reference CAS digest differs")
			}
			if !json.Valid(decoded.Bytes) {
				t.Fatal("canonical bytes are not JSON")
			}
			canonicalByName[fixture.Name] = decoded.Bytes
			again := report.Decode(decoded.Bytes, visibility())
			if !bytes.Equal(again.Bytes, decoded.Bytes) {
				t.Fatalf("normalization is not idempotent: %s", again.Bytes)
			}
		})
	}
	if bytes.Equal(canonicalByName["advice-authored-order"], canonicalByName["advice-reversed-order"]) {
		t.Fatal("authored advice order was lost")
	}
}

func TestRefusedHasNoCanonicalBytes(t *testing.T) {
	for _, input := range []string{`{`, `{"unitId":"u","diagnostics":[null]}`, `{"unitId":"u","diagnostics":[],"extra":"/work/leak"}`} {
		decoded := report.Decode([]byte(input), visibility())
		if decoded.Refused == "" || decoded.Bytes != nil {
			t.Fatalf("refusal exposes CAS bytes: %+v", decoded)
		}
	}
}
