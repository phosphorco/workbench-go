package contextdaemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
)

func TestRuntimeDoesNotInvokeProfileOnlyProviderForContributions(t *testing.T) {
	runtime := &Runtime{
		providers:   make(map[providerKey]*providerState),
		connections: make(chan struct{}, 1),
	}
	activation := contextapi.ActivationResult{
		Scope: contextapi.ScopeIdentity{ID: "scope", CanonicalRoot: t.TempDir()},
		Effective: contextapi.EffectiveConfiguration{
			ConfigDigest: "sha256:activation",
			Providers: []contextapi.ProviderConfig{{
				ID:           "profile-only",
				Kind:         contextapi.ProviderKindExecutable,
				Executable:   "/definitely/not-a-provider",
				Capabilities: []contextapi.ProviderCapability{contextapi.ProviderCapabilityProfile},
			}},
		},
	}
	_, _, reasons := runtime.collectContributions(context.Background(), activation, ObserveInput{}, contextapi.Audience{ID: "audience"}, contextapi.ProfileSnapshot{}, time.Now().UTC())
	if len(runtime.providers) != 0 {
		t.Fatalf("profile-only provider was acquired for contribution: %#v", runtime.providers)
	}
	if len(reasons) != 1 || !strings.Contains(reasons[0].Summary, "does not declare contribute") {
		t.Fatalf("unexpected profile-only dispatch result: %#v", reasons)
	}
}
