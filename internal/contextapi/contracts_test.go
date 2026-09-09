package contextapi

import (
	"encoding/json"
	"testing"
)

func TestProjectDeclarationUsesExplicitValueDefaults(t *testing.T) {
	declaration := ProjectDeclaration{
		Enabled:      true,
		Scope:        DeclarationScopeSubtree,
		Contributors: map[ContributorName]Contributor{},
		Profile:      ProfileSelection{},
	}
	if !declaration.Enabled || declaration.Scope != DeclarationScopeSubtree || len(declaration.Contributors) != 0 {
		t.Fatalf("declaration defaults = %#v", declaration)
	}
	encoded, err := json.Marshal(declaration)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "null" || !json.Valid(encoded) {
		t.Fatalf("declaration encoded as invalid/absent value: %s", encoded)
	}
	if string(encoded) == "" || containsJSONField(encoded, "profileProviders") {
		t.Fatalf("declaration retained removed profile-provider field: %s", encoded)
	}
}

func TestContributorCapabilitiesDeriveProfileSelection(t *testing.T) {
	builtin := Contributor{Enabled: true, Kind: ContributorKindAiContext}
	if got := CapabilitiesForContributor(builtin); len(got) != 1 || got[0] != ProviderCapabilityContribute {
		t.Fatalf("builtin capabilities = %#v", got)
	}
	executable := Contributor{Enabled: true, Kind: ContributorKindExecutable, Executable: Executable{
		Capabilities: []ProviderCapability{ProviderCapabilityProfile, ProviderCapabilityContribute},
	}}
	got := CapabilitiesForContributor(executable)
	if len(got) != 2 || got[0] != ProviderCapabilityProfile || got[1] != ProviderCapabilityContribute {
		t.Fatalf("executable capabilities = %#v", got)
	}
	executable.Enabled = false
	if got := CapabilitiesForContributor(executable); len(got) != 0 {
		t.Fatalf("disabled capabilities = %#v", got)
	}
}

func TestFlatPklContributorProjectsToTypedValueWithoutLosingSettings(t *testing.T) {
	input := evaluatedContributor{
		Enabled:      true,
		Kind:         ContributorKindExecutable,
		Executable:   "/opt/provider",
		Arguments:    []string{"--direct"},
		Capabilities: []ProviderCapability{ProviderCapabilityProfile},
		Settings:     json.RawMessage(`{"":null,"nested":{"items":[true,2.5]}}`),
	}
	got, err := projectContributorValue(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Executable.Executable != input.Executable || len(got.Executable.Arguments) != 1 || len(got.Executable.Capabilities) != 1 || string(got.Executable.Settings) != string(input.Settings) {
		t.Fatalf("projected contributor = %#v", got)
	}
	input.Settings = nil
	got, err = projectContributorValue(input)
	if err != nil || string(got.Executable.Settings) != `{}` {
		t.Fatalf("empty settings projection = %#v, err = %v", got.Executable.Settings, err)
	}
}

func containsJSONField(encoded []byte, field string) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return false
	}
	_, ok := fields[field]
	return ok
}

func TestSuccessfulRPCResponseOmitsErrorObject(t *testing.T) {
	encoded, err := json.Marshal(JSONRPCResponse{JSONRPC: "2.0", ID: 1, Result: json.RawMessage(`{"accepted":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"jsonrpc":"2.0","id":1,"result":{"accepted":true}}` {
		t.Fatalf("success response = %s", encoded)
	}
}

func TestHomeSnapshotCarriesHomeOnlyBounds(t *testing.T) {
	snapshot := HomeSnapshot{
		Delivery: EngineLimits{Queue: QueueLimits{MaxPendingItems: 3}, MaxRetainedBytes: 4096},
		Runtime:  RuntimeLimits{WholeHookDeadlineMs: 500, IdleTTLMs: 1000},
		Cache:    CacheLimits{DiskCapBytes: 5_000_000_000, MaxQueryRecords: 20},
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded HomeSnapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Delivery.Queue.MaxPendingItems != 3 || decoded.Delivery.MaxRetainedBytes != 4096 || decoded.Runtime.IdleTTLMs != 1000 || decoded.Cache.MaxQueryRecords != 20 {
		t.Fatalf("home bounds did not round trip: %#v", decoded)
	}
}
