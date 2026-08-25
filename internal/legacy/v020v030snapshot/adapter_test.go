package v020v030snapshot

import (
	"strings"
	"testing"
)

func TestContractURIAcceptsOnlyImmutableCompatibleReleases(t *testing.T) {
	for _, version := range []string{"0.2.0", "0.3.0"} {
		uri, err := ContractURI(version)
		if err != nil {
			t.Fatal(err)
		}
		want := "/releases/download/" + version + "/workbench@" + version + "#/" + Filename
		if !strings.HasSuffix(uri, want) {
			t.Fatalf("URI %q does not end with %q", uri, want)
		}
	}
	for _, version := range []string{"0.1.0", "0.4.0", "0.3.1", ""} {
		if _, err := ContractURI(version); err == nil {
			t.Fatalf("unsupported release %q was accepted", version)
		}
	}
}

func TestDecodePreservesReleasedSnapshotMeaning(t *testing.T) {
	value, err := Decode([]byte(`{
  "resources": {
    "phosphorco/library": {
      "shape": {"kind": "repository"},
      "github": "phosphorco/library",
      "canonicalPath": "repos/library",
      "commit": "0123456789abcdef0123456789abcdef01234567"
    }
  }
}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Resources) != 1 {
		t.Fatalf("resource count = %d", len(value.Resources))
	}
}
