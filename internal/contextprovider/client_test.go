package contextprovider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
	"github.com/phosphorco/workbench-go/internal/contextconfig"
)

// TestContextProviderHelper is also the executable used by the subprocess
// tests. It intentionally contains providers that violate one transport rule
// at a time: these are the counterexamples the client must contain.
func TestContextProviderHelper(t *testing.T) {
	mode, ok := helperMode()
	if !ok {
		return
	}
	encoder := json.NewEncoder(os.Stdout)
	decoder := json.NewDecoder(bufio.NewReader(os.Stdin))
	for {
		var request contextapi.JSONRPCRequest
		if err := decoder.Decode(&request); err != nil {
			return
		}
		switch request.Method {
		case contextapi.RPCMethodInitialize:
			writeHelperResponse(encoder, request.ID, contextapi.ProviderInitializeResponse{
				Accepted:     true,
				Capabilities: []contextapi.ProviderCapability{contextapi.ProviderCapabilityProfile, contextapi.ProviderCapabilityContribute},
			})
			if mode == "grandchild" {
				marker := ""
				for index, argument := range os.Args {
					if argument == mode && index+1 < len(os.Args) {
						marker = os.Args[index+1]
						break
					}
				}
				child := exec.Command("sh", "-c", "sleep 30")
				if err := child.Start(); err != nil || marker == "" {
					os.Exit(3)
				}
				_ = os.WriteFile(marker, []byte(fmt.Sprintf("%d", child.Process.Pid)), 0600)
				blockHelper()
			}
			if mode == "block-before-contribute" {
				blockHelper()
			}
		case contextapi.RPCMethodProfile:
			profile := contextapi.ProfileResponse{
				Facts: []contextapi.ProfileFact{{
					Key:   "provider-fact",
					Value: contextapi.FactValue{Kind: contextapi.FactText, Text: "from-child"},
					Provenance: contextapi.FactProvenance{
						Origin: contextapi.FactProvider,
					},
				}},
			}
			if mode == "bad-profile" {
				profile.Facts[0].Provenance.Origin = contextapi.FactHost
			}
			writeHelperResponse(encoder, request.ID, contextapi.ProviderProfileResponse{Output: profile})
		case contextapi.RPCMethodContribute:
			if mode == "ignore-contribute" || mode == "block-before-contribute" {
				blockHelper()
			}
			if mode == "spam-stdout" {
				_, _ = io.WriteString(os.Stdout, strings.Repeat("x", 2048))
				blockHelper()
			}
			if mode == "malformed" {
				_, _ = io.WriteString(os.Stdout, "not-json\n")
				blockHelper()
			}
			if mode == "spam-stderr" {
				_, _ = io.WriteString(os.Stderr, strings.Repeat("e", 2048))
			}
			output := contextapi.ContributionResponse{
				Reasons: []contextapi.Reason{{
					Origin:   contextapi.ReasonRuntime,
					Provider: "other-provider",
					Summary:  "child supplied authority",
				}},
				Contributions: []contextapi.Contribution{{
					Slot: contextapi.ContributionSlot{Key: "child-slot"},
					Source: contextapi.SourceRef{
						Identity: contextapi.SourceIdentity{ID: "child-source"},
						Kind:     contextapi.SourceFile,
						Path:     "child.md",
					},
					Body: "exact child body é\n",
					Reasons: []contextapi.Reason{{
						Origin:   contextapi.ReasonConfigured,
						Provider: "other-provider",
						Summary:  "child supplied contribution authority",
					}},
				}},
			}
			if mode != "bad-reason" {
				output.Reasons = nil
				output.Contributions[0].Reasons = nil
			}
			responseID := request.ID
			if mode == "wrong-id" {
				responseID++
			}
			writeHelperResponse(encoder, responseID, contextapi.ProviderContributeResponse{Output: output})
			if mode == "exit-after-contribute" {
				os.Exit(0)
			}
		case contextapi.RPCMethodShutdown:
			if mode == "ignore-shutdown" {
				blockHelper()
			}
			writeHelperResponse(encoder, request.ID, contextapi.ProviderShutdownResponse{RequestID: request.ID})
			return
		}
	}
}

func helperMode() (string, bool) {
	for index, argument := range os.Args {
		if argument == "--" && index+1 < len(os.Args) {
			return os.Args[index+1], true
		}
	}
	return "", false
}

func writeHelperResponse(encoder *json.Encoder, id contextapi.RequestID, result any) {
	response := contextapi.JSONRPCResponse{JSONRPC: "2.0", ID: id}
	response.Result, _ = json.Marshal(result)
	_ = encoder.Encode(response)
}

func blockHelper() {
	for {
		time.Sleep(time.Hour)
	}
}

func testClientConfig(t *testing.T, mode string) (contextapi.ProviderConfig, contextapi.ProviderResource) {
	t.Helper()
	root := t.TempDir()
	config := contextapi.ProviderConfig{
		ID:           "test-provider",
		Kind:         contextapi.ProviderKindExecutable,
		Executable:   os.Args[0],
		Arguments:    []string{"-test.run=TestContextProviderHelper", "--", mode},
		Capabilities: []contextapi.ProviderCapability{contextapi.ProviderCapabilityProfile, contextapi.ProviderCapabilityContribute},
		Limits: contextapi.ProviderLimits{
			MaxResponseBytes: 8 << 20,
			MaxFacts:         4,
			MaxContributions: 4,
			MaxBodyBytes:     1 << 20,
		},
	}
	resource := contextapi.ProviderResource{
		Provider: config.ID,
		Scope: contextapi.ScopeIdentity{
			ID:            "scope",
			Authority:     contextapi.ScopeAuthorityProject,
			CanonicalRoot: root,
			ConfigDigest:  "sha256:scope-declaration",
		},
		ConfigDigest: "sha256:effective-config",
	}
	return config, resource
}

func testContributionRequest() contextapi.ContributionRequest {
	return contextapi.ContributionRequest{
		RequestID: 2,
		Audience:  contextapi.Audience{ID: "audience", Epoch: 1},
		Observation: contextapi.Observation{
			ID: 9,
			At: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		},
		Profile: contextapi.ProfileSnapshot{Revision: "profile:1"},
		Limits:  contextapi.ContributionLimits{MaxContributions: 2, MaxBodyBytes: 1000, MaxReasonBytes: 1 << 16},
	}
}

func TestClientHydratesProviderOwnedContributionAndProfileAuthority(t *testing.T) {
	config, resource := testClientConfig(t, "normal")
	client, err := Start(context.Background(), config, resource, Options{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := client.Profile(context.Background(), contextapi.ProfileRequest{
		Audience: contextapi.Audience{ID: "audience", Epoch: 1},
		Limits:   contextapi.ProfileLimits{MaxFacts: 1, MaxReasonBytes: 1 << 16},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Facts) != 1 || profile.Facts[0].Provenance.Provider != config.ID || profile.Facts[0].Provenance.Origin != contextapi.FactProvider {
		t.Fatalf("profile provenance was not normalized: %#v", profile)
	}
	output, err := client.Contribute(context.Background(), testContributionRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Contributions) != 1 {
		t.Fatalf("got %d contributions, want one", len(output.Contributions))
	}
	contribution := output.Contributions[0]
	if contribution.Contributor != config.ID || contribution.Slot.Provider != config.ID || contribution.Source.Identity.Provider != config.ID {
		t.Fatalf("provider identity was not hydrated: %#v", contribution)
	}
	if contribution.ConfigDigest != resource.ConfigDigest || contribution.ProfileRevision != "profile:1" {
		t.Fatalf("runtime identity was not hydrated: %#v", contribution)
	}
	if contribution.Content.UTF8Bytes != uint64(len([]byte(contribution.Body))) || contribution.Content.ID == "" || contribution.SourceRevision.Source != contribution.Source.Identity || contribution.SourceRevision.Revision == "" {
		t.Fatalf("complete content identity was not hydrated: %#v", contribution)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats := client.Stats()
	if !stats.Closed || !stats.ProcessExited || stats.Requests != 4 || stats.Responses != 4 {
		t.Fatalf("unexpected stats after initialize/profile/contribute/shutdown: %#v", stats)
	}
}

func TestLoadedActivationPassesIndependentDigestsToProviderAndBuiltin(t *testing.T) {
	root := t.TempDir()
	loadedProvider := contextapi.ProviderConfig{
		ID:           "loaded-provider",
		Kind:         contextapi.ProviderKindExecutable,
		Executable:   os.Args[0],
		Arguments:    []string{"-test.run=TestContextProviderHelper", "--", "normal"},
		Capabilities: []contextapi.ProviderCapability{contextapi.ProviderCapabilityContribute},
		Limits: contextapi.ProviderLimits{
			MaxResponseBytes: 1 << 20,
			MaxContributions: 2,
			MaxBodyBytes:     64 << 10,
		},
	}
	project := contextapi.ProjectFile{
		SchemaVersion:   1,
		OptIn:           true,
		IncludeChildren: true,
		Providers:       []contextapi.ProviderConfig{loadedProvider},
	}
	raw, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	writeBuiltinFile(t, root, ".workbench/context.json", string(raw))
	loaded, err := contextconfig.Load(contextconfig.LoadOptions{WorkingDirectory: root, Now: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Result.State != contextapi.ActivationEnabled || len(loaded.Result.Effective.Providers) != 1 {
		t.Fatalf("real activation did not load fixture provider: state=%q result=%#v", loaded.Result.State, loaded.Result)
	}
	if loaded.Result.Scope.ConfigDigest == "" || loaded.Result.Effective.ConfigDigest == "" || loaded.Result.Scope.ConfigDigest == loaded.Result.Effective.ConfigDigest {
		t.Fatalf("real loader did not produce independent declaration/effective digests: scope=%q effective=%q", loaded.Result.Scope.ConfigDigest, loaded.Result.Effective.ConfigDigest)
	}
	resource := contextapi.ProviderResource{
		Provider: loaded.Result.Effective.Providers[0].ID,
		Scope:    loaded.Result.Effective.Scope,
		// Effective activation identity is deliberately distinct from Scope.ConfigDigest.
		ConfigDigest: loaded.Result.Effective.ConfigDigest,
	}
	client, err := Start(context.Background(), loaded.Result.Effective.Providers[0], resource, Options{})
	if err != nil {
		t.Fatal(err)
	}
	request := testContributionRequest()
	request.Scope = loaded.Result.Effective.Scope
	request.ConfigDigest = loaded.Result.Effective.ConfigDigest
	output, err := client.Contribute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Contributions) != 1 || output.Contributions[0].ConfigDigest != loaded.Result.Effective.ConfigDigest {
		t.Fatalf("provider did not preserve effective digest independently: %#v", output)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	writeBuiltinFile(t, root, "ai-context.md", "---\ndocs:\n  - message: loaded builtin\n---\n")
	builtinRequest := builtinRequest([]contextapi.ObservedResource{observedFile("source.go", contextapi.ResourceOutcomeUnknown, contextapi.ConfidenceInferred)}, nil)
	builtinRequest.Scope = loaded.Result.Effective.Scope
	builtinRequest.ConfigDigest = loaded.Result.Effective.ConfigDigest
	builtinResponse := ContributeBuiltin(context.Background(), builtinRequest, root, builtinTestLimits())
	if len(builtinResponse.Contributions) != 1 || builtinResponse.Contributions[0].ConfigDigest != loaded.Result.Effective.ConfigDigest {
		t.Fatalf("builtin did not preserve effective digest independently: %#v", builtinResponse)
	}
}

func TestClientNormalizesProviderReasonAuthority(t *testing.T) {
	config, resource := testClientConfig(t, "bad-reason")
	client, err := Start(context.Background(), config, resource, Options{})
	if err != nil {
		t.Fatal(err)
	}
	output, err := client.Contribute(context.Background(), testContributionRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Reasons) != 1 || output.Reasons[0].Origin != contextapi.ReasonProvider || output.Reasons[0].Provider != config.ID {
		t.Fatalf("top-level child reason claimed non-provider authority: %#v", output.Reasons)
	}
	if len(output.Contributions) != 1 || len(output.Contributions[0].Reasons) != 1 || output.Contributions[0].Reasons[0].Origin != contextapi.ReasonProvider || output.Contributions[0].Reasons[0].Provider != config.ID {
		t.Fatalf("contribution child reason claimed non-provider authority: %#v", output.Contributions)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	config, resource = testClientConfig(t, "bad-profile")
	client, err = Start(context.Background(), config, resource, Options{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := client.Profile(context.Background(), contextapi.ProfileRequest{Limits: contextapi.ProfileLimits{MaxFacts: 1, MaxReasonBytes: 1 << 16}})
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Facts) != 0 || len(profile.Reasons) != 1 || profile.Reasons[0].Origin != contextapi.ReasonProvider || profile.Reasons[0].Provider != config.ID {
		t.Fatalf("malformed profile fact reason was not provider-authored: %#v", profile)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClientEnforcesProviderAndRequestSemanticBoundsPerItem(t *testing.T) {
	_, resource := testClientConfig(t, "normal")
	request := testContributionRequest()
	request.Limits.MaxContributions = 2
	input := contextapi.ContributionResponse{Contributions: []contextapi.Contribution{
		{Slot: contextapi.ContributionSlot{Key: "empty"}, Source: contextapi.SourceRef{Identity: contextapi.SourceIdentity{ID: "empty"}}, Body: ""},
		{Slot: contextapi.ContributionSlot{Key: "valid"}, Source: contextapi.SourceRef{Identity: contextapi.SourceIdentity{ID: "valid"}}, Body: "kept"},
	}}
	output, err := normalizeContributionResponse(input, request, resource, contextapi.ProviderLimits{MaxContributions: 1, MaxBodyBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Contributions) != 1 || output.Contributions[0].Body != "kept" || len(output.Reasons) == 0 {
		t.Fatalf("malformed item or minimum contribution bound was not isolated: %#v", output)
	}

	profileRequest := contextapi.ProfileRequest{Limits: contextapi.ProfileLimits{MaxFacts: 2}}
	_, err = normalizeProfileResponse(contextapi.ProfileResponse{Facts: []contextapi.ProfileFact{
		{Key: "one", Value: contextapi.FactValue{Kind: contextapi.FactText, Text: "1"}},
		{Key: "two", Value: contextapi.FactValue{Kind: contextapi.FactText, Text: "2"}},
	}}, profileRequest, resource.Provider, 1)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("provider fact bound was not enforced: %v", err)
	}
}

func TestClientReadsFinalResponseBeforeProviderExit(t *testing.T) {
	config, resource := testClientConfig(t, "exit-after-contribute")
	client, err := Start(context.Background(), config, resource, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Contribute(context.Background(), testContributionRequest()); err != nil {
		t.Fatalf("final response was lost when child exited: %v", err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClientRejectsBoundAndProtocolViolations(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		options Options
		wantErr error
	}{
		{name: "stdout-bound", mode: "spam-stdout", options: Options{MaxStdoutBytes: 128}, wantErr: ErrResponseTooLarge},
		{name: "wrong-id", mode: "wrong-id", wantErr: &ProtocolError{}},
		{name: "malformed", mode: "malformed", wantErr: &ProtocolError{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, resource := testClientConfig(t, test.mode)
			client, err := Start(context.Background(), config, resource, test.options)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Contribute(context.Background(), testContributionRequest())
			if !errors.Is(err, test.wantErr) {
				var protocolErr *ProtocolError
				if _, ok := test.wantErr.(*ProtocolError); !(ok && errors.As(err, &protocolErr)) {
					t.Fatalf("got error %v, want %v", err, test.wantErr)
				}
			}
			if closeErr := client.Close(context.Background()); closeErr != nil {
				t.Fatalf("close after protocol violation: %v", closeErr)
			}
		})
	}
}

func TestClientDrainsBoundedStderrWithoutRetainingIt(t *testing.T) {
	config, resource := testClientConfig(t, "spam-stderr")
	client, err := Start(context.Background(), config, resource, Options{MaxStderrBytes: 128, MaxStderrRead: 32})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Contribute(context.Background(), testContributionRequest()); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats := client.Stats()
	if stats.StderrBytes < 2048 || !stats.StderrTruncated {
		t.Fatalf("stderr was not fully drained and bounded: %#v", stats)
	}
}

func TestClientCancellationKillsBlockedRequestAndJoinsReaders(t *testing.T) {
	config, resource := testClientConfig(t, "ignore-contribute")
	client, err := Start(context.Background(), config, resource, Options{})
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = client.Contribute(requestContext, testContributionRequest())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want deadline exceeded", err)
	}
	closeStart := time.Now()
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(closeStart); elapsed > time.Second {
		t.Fatalf("close did not join killed provider promptly: %s", elapsed)
	}
}

func TestClientCancellationUnblocksBlockedStdin(t *testing.T) {
	config, resource := testClientConfig(t, "block-before-contribute")
	client, err := Start(context.Background(), config, resource, Options{MaxRequestBytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	request := testContributionRequest()
	request.Observation.Facts = []contextapi.Fact{{Key: "large", Value: contextapi.FactValue{Kind: contextapi.FactText, Text: strings.Repeat("x", 2<<20)}}}
	requestContext, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = client.Contribute(requestContext, request)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want deadline exceeded", err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClientCancellationWatcherIsJoinedAcrossRapidCalls(t *testing.T) {
	config, resource := testClientConfig(t, "normal")
	client, err := Start(context.Background(), config, resource, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 100; index++ {
		request := testContributionRequest()
		request.RequestID = contextapi.RequestID(index + 10)
		if _, err := client.Contribute(context.Background(), request); err != nil {
			t.Fatalf("call %d: %v", index, err)
		}
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClientKillsAProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child.pid")
	config, resource := testClientConfig(t, "grandchild")
	config.Arguments = append(config.Arguments, marker)
	client, err := Start(context.Background(), config, resource, Options{})
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _ = client.Contribute(requestContext, testContributionRequest())
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("grandchild process %d survived process-group cleanup", pid)
}

func TestClientCloseWithBackgroundBoundsIgnoredShutdown(t *testing.T) {
	config, resource := testClientConfig(t, "ignore-shutdown")
	client, err := Start(context.Background(), config, resource, Options{})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > shutdownDeadline+time.Second {
		t.Fatalf("background close exceeded finite shutdown bound: %s", elapsed)
	}
}
