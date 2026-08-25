package setup

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestLegacyV010V030ManagedCheckoutsMigrateWithoutLosingProvenance(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, ".workbench")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(state, "world.json")
	write(t, legacy, `{"version":1,"resources":[{"identity":"phosphorco/retired","github":"phosphorco/retired","shape":{"kind":"repository"},"canonicalPath":"repos/retired","createdByWorkbench":true},{"identity":"@entry","github":"phosphorco/entry","shape":{"kind":"packageScope","scope":"@entry"},"canonicalPath":"pkg/@entry","createdByWorkbench":false}]}`)

	plan, err := preflightManagedCheckoutMigration(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy receipt still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "managed-checkouts.json")); err != nil {
		t.Fatal(err)
	}
	checkouts, err := ReadManagedCheckouts(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(checkouts) != 2 || checkouts[0].Identity != "@entry" || checkouts[0].CreatedByWorkbench || checkouts[1].Identity != "phosphorco/retired" || !checkouts[1].CreatedByWorkbench {
		t.Fatalf("managed checkouts = %#v", checkouts)
	}
}

func TestLegacyV010V030ManagedCheckoutPreflightRefusesWithoutMutation(t *testing.T) {
	tests := map[string]struct {
		current string
		legacy  string
	}{
		"malformed legacy": {legacy: `{"version":1,"resources":[],"unknown":true}`},
		"ambiguous dual state": {
			current: `{"version":1,"resources":[]}`,
			legacy:  `{"version":1,"resources":[{"identity":"phosphorco/entry","github":"phosphorco/entry","shape":{"kind":"repository"},"canonicalPath":"repos/entry","createdByWorkbench":true}]}`,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, ".workbench")
			if err := os.Mkdir(state, 0o700); err != nil {
				t.Fatal(err)
			}
			if test.current != "" {
				write(t, filepath.Join(state, "managed-checkouts.json"), test.current)
			}
			if test.legacy != "" {
				write(t, filepath.Join(state, "world.json"), test.legacy)
			}
			before := readTree(t, root)
			if _, err := preflightManagedCheckoutMigration(root); err == nil {
				t.Fatal("unsafe state was accepted")
			}
			after := readTree(t, root)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("preflight mutated state:\nbefore=%q\nafter=%q", before, after)
			}
		})
	}
}

func TestLegacyV010V030ManagedCheckoutSymlinkRefusesWithoutMutation(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, ".workbench")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "foreign.json")
	write(t, target, `{"version":1,"resources":[]}`)
	if err := os.Symlink(target, filepath.Join(state, "world.json")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preflightManagedCheckoutMigration(root); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink refusal = %v", err)
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, after) {
		t.Fatal("symlink target changed")
	}
}

func TestLegacyV010V030EqualDualStateConvergesToCurrentIdentity(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, ".workbench")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	current := `{"version":1,"resources":[{"identity":"phosphorco/entry","github":"phosphorco/entry","shape":{"kind":"repository"},"canonicalPath":"repos/entry","createdByWorkbench":true}]}`
	legacy := `{
  "resources": [{"createdByWorkbench": true, "canonicalPath":"repos/entry", "shape":{"kind":"repository"}, "github":"phosphorco/entry", "identity":"phosphorco/entry"}],
  "version": 1
}`
	write(t, filepath.Join(state, "managed-checkouts.json"), current)
	write(t, filepath.Join(state, "world.json"), legacy)
	plan, err := preflightManagedCheckoutMigration(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(state, "world.json")); !os.IsNotExist(err) {
		t.Fatalf("equal historical identity remains: %v", err)
	}
	if encoded, err := os.ReadFile(filepath.Join(state, "managed-checkouts.json")); err != nil {
		t.Fatal(err)
	} else if string(encoded) != current {
		t.Fatal("equal current state was unnecessarily rewritten")
	}
}
