package version

import "testing"

func TestCurrentRejectsUninjectedRelease(t *testing.T) {
	previousVersion, previousRevision := release, revision
	t.Cleanup(func() { release, revision = previousVersion, previousRevision })
	release, revision = "dev", "unknown"

	if _, err := Current(); err == nil {
		t.Fatal("Current() succeeded without release injection")
	}
}

func TestCurrentReportsExactReleaseAndRevision(t *testing.T) {
	previousVersion, previousRevision := release, revision
	t.Cleanup(func() { release, revision = previousVersion, previousRevision })
	release = "0.2.0"
	revision = "0123456789abcdef0123456789abcdef01234567"

	got, err := Current()
	if err != nil {
		t.Fatalf("Current(): %v", err)
	}
	if got.Release != release || got.Revision != revision {
		t.Fatalf("Current() = %#v, want release %q revision %q", got, release, revision)
	}
	if got.String() != "workbench 0.2.0 (0123456789abcdef0123456789abcdef01234567)" {
		t.Fatalf("Info.String() = %q", got.String())
	}
}

func TestIsDevelopmentRecognizesOnlyExactDefaults(t *testing.T) {
	previousVersion, previousRevision := release, revision
	t.Cleanup(func() { release, revision = previousVersion, previousRevision })

	for _, test := range []struct {
		name        string
		release     string
		revision    string
		development bool
	}{
		{name: "defaults", release: "dev", revision: "unknown", development: true},
		{name: "release only", release: "0.2.0", revision: "unknown"},
		{name: "revision only", release: "dev", revision: "0123456789abcdef0123456789abcdef01234567"},
		{name: "malformed", release: "development", revision: "unknown"},
		{name: "released", release: "0.2.0", revision: "0123456789abcdef0123456789abcdef01234567"},
	} {
		t.Run(test.name, func(t *testing.T) {
			release, revision = test.release, test.revision
			if got := IsDevelopment(); got != test.development {
				t.Fatalf("IsDevelopment() = %t, want %t", got, test.development)
			}
		})
	}
}
