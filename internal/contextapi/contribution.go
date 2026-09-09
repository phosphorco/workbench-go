package contextapi

import "encoding/json"

type SourceKind string

const (
	SourceFile    SourceKind = "file"
	SourceSession SourceKind = "session"
	SourceTrigger SourceKind = "trigger"
	SourceOther   SourceKind = "other"
)

// SourceIdentity is the stable source key. Path and Label in SourceRef are
// attribution metadata and do not replace this identity.
type SourceIdentity struct {
	Provider ProviderID `json:"provider"`
	ID       SourceID   `json:"id"`
}

type SourceRef struct {
	Identity SourceIdentity `json:"identity"`
	Kind     SourceKind     `json:"kind"`
	Path     string         `json:"path,omitempty"`
	Label    string         `json:"label,omitempty"`
}

type SourceRevision struct {
	Source   SourceIdentity   `json:"source"`
	Revision SourceRevisionID `json:"revision"`
}

type SourceRecruitmentKind string

const (
	RecruitmentObservedFile SourceRecruitmentKind = "observed-file"
	RecruitmentSelectors    SourceRecruitmentKind = "selectors"
)

// Recruitment is causal context, not source identity or semantic content.
type SourceRecruitment struct {
	Kind     SourceRecruitmentKind `json:"kind"`
	Resource string                `json:"resource,omitempty"`
}

type ContentIdentity struct {
	ID        ContentID `json:"id"`
	UTF8Bytes uint64    `json:"utf8Bytes"`
}

type ContributionSlot struct {
	Provider ProviderID `json:"provider"`
	Key      SlotKey    `json:"key"`
}

// A Contribution is a complete source block, not a trace sample. Body is the
// exact UTF-8 material eligible for delivery and must never be reconstructed
// from trace samples.
type Contribution struct {
	Slot            ContributionSlot  `json:"slot"`
	Source          SourceRef         `json:"source"`
	SourceRevision  SourceRevision    `json:"sourceRevision"`
	Content         ContentIdentity   `json:"content"`
	Body            string            `json:"body"`
	Recruitment     SourceRecruitment `json:"recruitment,omitempty"`
	Contributor     ProviderID        `json:"contributor"`
	ConfigDigest    ConfigDigest      `json:"configDigest"`
	ProfileRevision ProfileRevision   `json:"profileRevision"`
	Priority        int32             `json:"priority"`
	Reasons         []Reason          `json:"reasons,omitempty"`
}

type ContributionLimits struct {
	MaxContributions uint32 `json:"maxContributions"`
	MaxBodyBytes     uint64 `json:"maxBodyBytes"`
	MaxReasonBytes   uint64 `json:"maxReasonBytes"`
}

type ContributionRequest struct {
	RequestID    RequestID          `json:"requestId"`
	Scope        ScopeIdentity      `json:"scope"`
	Audience     Audience           `json:"audience"`
	Task         TaskRef            `json:"task"`
	Observation  Observation        `json:"observation"`
	Profile      ProfileSnapshot    `json:"profile"`
	ConfigDigest ConfigDigest       `json:"configDigest"`
	Limits       ContributionLimits `json:"limits"`
}

type ContributionResponse struct {
	Contributions []Contribution `json:"contributions"`
	Reasons       []Reason       `json:"reasons"`
}

// AIDocumentRule and AICommandRule preserve the existing builtin ai-context
// frontmatter vocabulary. The builtin provider owns parsing TOML/YAML and
// selection; these values are not a second document format.
type AIDocumentRule struct {
	Files    []string `json:"files,omitempty"`
	Mentions []string `json:"mentions,omitempty"`
	Include  []string `json:"include,omitempty"`
	Message  string   `json:"message,omitempty"`
}

type AICommandRule struct {
	Files    []string `json:"files,omitempty"`
	Mentions []string `json:"mentions,omitempty"`
	Command  string   `json:"command"`
	Label    string   `json:"label,omitempty"`
	CWD      string   `json:"cwd,omitempty"`
}

type AIContextManifest struct {
	Root     bool             `json:"root,omitempty"`
	Docs     []AIDocumentRule `json:"docs,omitempty"`
	Commands []AICommandRule  `json:"commands,omitempty"`
}

// JSONRPCRequest/Response are only the transport envelope. Params and Result
// are decoded into the typed method schemas below; RawMessage is the single
// opaque JSON boundary in this package.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      RequestID       `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      RequestID       `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

const (
	RPCMethodInitialize = "initialize"
	RPCMethodProfile    = "audience.profile"
	RPCMethodContribute = "context.contribute"
	RPCMethodShutdown   = "shutdown"
)

type ProviderInitializeRequest struct {
	ProtocolVersion       uint32               `json:"protocolVersion"`
	Resource              ProviderResource     `json:"resource"`
	Settings              json.RawMessage      `json:"settings,omitempty"`
	SupportedCapabilities []ProviderCapability `json:"supportedCapabilities"`
	Limits                ProviderLimits       `json:"limits"`
}

type ProviderInitializeResponse struct {
	Accepted     bool                 `json:"accepted"`
	Capabilities []ProviderCapability `json:"capabilities"`
	Reasons      []Reason             `json:"reasons"`
}

// ProviderResource identifies the runtime-owned resource being called. It is
// explicit on every non-shutdown provider operation so a reused process cannot
// accidentally apply one scope's facts to another.
type ProviderResource struct {
	Provider     ProviderID    `json:"provider"`
	Scope        ScopeIdentity `json:"scope"`
	ConfigDigest ConfigDigest  `json:"configDigest"`
}

type ProviderProfileRequest struct {
	Resource ProviderResource `json:"resource"`
	Input    ProfileRequest   `json:"input"`
}

type ProviderProfileResponse struct {
	Output ProfileResponse `json:"output"`
}

type ProviderContributeRequest struct {
	Resource ProviderResource    `json:"resource"`
	Input    ContributionRequest `json:"input"`
}

type ProviderContributeResponse struct {
	Output ContributionResponse `json:"output"`
}

type ProviderShutdownRequest struct {
	RequestID RequestID `json:"requestId"`
}

type ProviderShutdownResponse struct {
	RequestID RequestID `json:"requestId"`
	Reasons   []Reason  `json:"reasons"`
}
