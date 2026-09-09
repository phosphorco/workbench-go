// Package contextdaemon composes the Workbench context contracts into one
// per-OS-user runtime. The package owns transport and lifecycle state; it does
// not make the harness adapter part of the daemon API.
package contextdaemon

import (
	"context"
	"os"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
	"github.com/phosphorco/workbench-go/internal/contextconfig"
	"github.com/phosphorco/workbench-go/internal/contextengine"
	"github.com/phosphorco/workbench-go/internal/contexttrace"
)

// Paths names every filesystem boundary used by a runtime. Callers should
// provide these explicitly, especially acceptance tests. A path is not
// inferred from the working directory, and the runtime never writes project
// files.
type Paths struct {
	// HomeConfigPath is the authored user-home configuration. An empty value
	// means that no home declaration participates in activation.
	HomeConfigPath string
	// RuntimeDir contains the private socket and startup lock when their
	// individual paths are not supplied.
	RuntimeDir     string
	SocketPath     string
	LockPath       string
	ServerLockPath string
	// CacheDir is the private explanation cache directory. It is independent
	// from runtime residency and is the only path opened by contexttrace.
	CacheDir string
}

// DefaultPaths derives conventional user paths from an explicit private
// directory. It does not inspect the environment or the current directory.
// The returned values are a convenience for the CLI; tests should generally
// construct Paths directly beneath a temporary directory.
func DefaultPaths(privateDir string) Paths {
	return Paths{
		RuntimeDir:     privateDir,
		SocketPath:     privateDir + "/context.sock",
		LockPath:       privateDir + "/context.start.lock",
		ServerLockPath: privateDir + "/context.server.lock",
		CacheDir:       privateDir + "/cache",
	}
}

// ObserveInput is the complete normalized hook-to-runtime observation. The
// hook performs activation locally before Ensure; the daemon receives the
// working directory and re-loads current authority from Paths.HomeConfigPath.
type ObserveInput struct {
	WorkingDirectory string                         `json:"workingDirectory"`
	Host             contextapi.HostSnapshot        `json:"host"`
	Observation      contextapi.Observation         `json:"observation"`
	Opportunity      contextapi.DeliveryOpportunity `json:"opportunity"`
	Transition       contextapi.AudienceTransition  `json:"transition,omitempty"`
}

// ObserveResult returns the exact offer selected by the runtime. The adapter
// must hand off Offer.Body and then call Confirm with the unchanged identity;
// an offer is never a receipt.
type ObserveResult struct {
	Activation contextapi.ActivationResult  `json:"activation"`
	Decision   contextapi.DeliveryDecision  `json:"decision"`
	Generation contextapi.RuntimeGeneration `json:"generation"`
	Audience   contextapi.Audience          `json:"audience"`
}

// ConfirmInput is the adapter's post-handoff input. Profile revision,
// validity, scope, and configuration are runtime-owned derived values; the
// CLI must not manufacture them from a stale Observe response. WorkingDirectory
// is required because a scope root alone loses nested-directory applicability.
type ConfirmInput struct {
	WorkingDirectory string                    `json:"workingDirectory"`
	Identity         contextapi.OfferIdentity  `json:"identity"`
	Handoff          contextapi.HandoffOutcome `json:"handoff"`
	At               time.Time                 `json:"at,omitempty"`
}

// StatusRequest scopes status without starting providers or renewing runtime
// resource residency. An empty WorkingDirectory requests machine status.
type StatusRequest struct {
	WorkingDirectory string                   `json:"workingDirectory,omitempty"`
	Scope            contextapi.ScopeIdentity `json:"scope,omitempty"`
	Audience         contextapi.Audience      `json:"audience,omitempty"`
}

// PartitionStatus is a bounded snapshot. Engine counts are authoritative;
// runtime status does not maintain a second mutable queue or receipt count.
type PartitionStatus struct {
	Scope        contextapi.ScopeIdentity     `json:"scope"`
	Audience     contextapi.Audience          `json:"audience"`
	Generation   contextapi.RuntimeGeneration `json:"generation"`
	ConfigDigest contextapi.ConfigDigest      `json:"configDigest"`
	Engine       contextengine.Stats          `json:"engine"`
	Profile      contextapi.ProfileSnapshot   `json:"profile"`
	LastUsedAt   time.Time                    `json:"lastUsedAt"`
}

// Status is a non-authoritative operational view of runtime residency plus
// authoritative per-partition engine counts and trace-cache metadata.
type Status struct {
	Generation        contextapi.RuntimeGeneration `json:"generation"`
	SocketPath        string                       `json:"socketPath"`
	Running           bool                         `json:"running"`
	ActiveScopes      uint32                       `json:"activeScopes"`
	ActiveAudiences   uint32                       `json:"activeAudiences"`
	ProviderProcesses uint32                       `json:"providerProcesses"`
	TraceQueueItems   uint32                       `json:"traceQueueItems"`
	TraceQueueBytes   uint64                       `json:"traceQueueBytes"`
	TraceDropped      uint64                       `json:"traceDropped"`
	Trace             contexttrace.Stats           `json:"trace"`
	Partitions        []PartitionStatus            `json:"partitions"`
	Reasons           []contextapi.Reason          `json:"reasons,omitempty"`
}

// InspectKind selects one of the bounded trace inspection views.
type InspectKind string

const (
	InspectContribution InspectKind = "contribution"
	InspectTurn         InspectKind = "turn"
	InspectProfile      InspectKind = "profile"
)

// InspectRequest is deliberately typed around the three trace-owned lookup
// keys. Exactly one key must be supplied for the selected kind.
type InspectRequest struct {
	Kind           InspectKind        `json:"kind"`
	ContributionID uint64             `json:"contributionId,omitempty"`
	Turn           string             `json:"turn,omitempty"`
	Profile        string             `json:"profile,omitempty"`
	Query          contexttrace.Query `json:"query"`
}

// RuntimeOptions supplies all effects and bounds needed to construct one
// runtime. Zero values select implementation defaults only for numeric
// bounds; paths remain explicit because accidental ambient paths would break
// user isolation and acceptance-test isolation.
type RuntimeOptions struct {
	Paths Paths
	// LoadLimits may narrow the bounded activation lookup. Home runtime
	// limits narrow it further after the home file has been read.
	LoadLimits contextconfig.LoadLimits
	// HookDeadline bounds one direct Observe/Confirm operation. The transport's
	// WholeHookDeadline remains the outer request bound; neither is a queue
	// length.
	HookDeadline      time.Duration
	WholeHookDeadline time.Duration
	// TraceQueueItems and TraceQueueBytes bound retained handoff work before
	// the trace worker. Bodies are copied and sampled before enqueue.
	TraceQueueItems uint32
	TraceQueueBytes uint64
	// TraceOptions is an explicit test/embedding seam. Non-zero fields are
	// overlaid by the resolved home cache limits when the runtime starts.
	TraceOptions contexttrace.Options
}

// ClientOptions controls bounded connection and startup behavior. A Client
// never starts a daemon on construction; Ensure does so only when an enabled
// hook has already passed local activation.
type ClientOptions struct {
	Paths          Paths
	StartupTimeout time.Duration
	DialTimeout    time.Duration
	MaxWireBytes   int
	Starter        Starter
}

// Starter returns the started child process. Ensure owns startup failure: it
// terminates and waits that process when readiness fails. Once the socket is
// ready, process ownership transfers to the daemon's serve lifecycle; Client
// owns only its connection and therefore never kills the shared daemon on
// Client.Close.
type Starter func(ctx context.Context, paths Paths) (*os.Process, error)
