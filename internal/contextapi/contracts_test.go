package contextapi

import (
	"encoding/json"
	"testing"
)

func TestProjectProviderAbsenceDiffersFromExplicitEmpty(t *testing.T) {
	var absent ProjectFile
	if err := json.Unmarshal([]byte(`{"optIn":true}`), &absent); err != nil {
		t.Fatal(err)
	}
	if absent.Providers != nil {
		t.Fatalf("absent providers decoded as %#v", absent.Providers)
	}
	var disabled ProjectFile
	if err := json.Unmarshal([]byte(`{"optIn":true,"providers":[]}`), &disabled); err != nil {
		t.Fatal(err)
	}
	if disabled.Providers == nil || len(disabled.Providers) != 0 {
		t.Fatalf("explicit empty providers decoded as %#v", disabled.Providers)
	}
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
