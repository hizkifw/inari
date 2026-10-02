// Package acp is a client for the Agent Client Protocol as kon speaks it:
// newline-delimited JSON-RPC 2.0 with an agent subprocess, plus kon's
// extensions under the kon.kitsu.red namespace.
package acp

import "encoding/json"

// The wire types below are the parts of ACP v1 inari reads or writes, named as
// the schema names them.

// ProtocolVersion is the ACP major version inari speaks.
const ProtocolVersion = 1

// extension is kon's ACP namespace: the key of its capabilities in _meta, and
// with a leading underscore the prefix of its methods.
const extension = "kon.kitsu.red"

// Extension notifications and methods kon serves.
const (
	methodTurnStart = "_" + extension + "/turn_start"
	methodTurnEnd   = "_" + extension + "/turn_end"
	methodSteer     = "_" + extension + "/steer"
	methodJobs      = "_" + extension + "/jobs"
	methodKillJob   = "_" + extension + "/kill_job"
)

// Stop reasons a turn ends with.
const (
	StopEndTurn   = "end_turn"
	StopCancelled = "cancelled"
)

// JSON-RPC error codes inari tells apart.
const (
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
)

type initializeRequest struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities clientCapabilities `json:"clientCapabilities"`
	ClientInfo         implementation     `json:"clientInfo"`
}

type clientCapabilities struct {
	FS       fsCapabilities `json:"fs"`
	Terminal bool           `json:"terminal"`
	Meta     map[string]any `json:"_meta"`
}

// fsCapabilities is sent all false: inari serves no files to the agent.
type fsCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Agent is what initialize says about the agent.
type Agent struct {
	ProtocolVersion   int            `json:"protocolVersion"`
	AgentInfo         implementation `json:"agentInfo"`
	AgentCapabilities struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	} `json:"agentCapabilities"`
}

// Kon's extensions, as initialize lists them.
const (
	ExtensionInstructions = "instructions"
)

// Supports reports whether the agent lists one of kon's extensions.
func (a Agent) Supports(name string) bool {
	var ext map[string]bool
	json.Unmarshal(a.AgentCapabilities.Meta[extension], &ext)
	return ext[name]
}

type sessionRequest struct {
	SessionID  string     `json:"sessionId,omitempty"`
	CWD        string     `json:"cwd"`
	MCPServers []struct{} `json:"mcpServers"`
	Meta       any        `json:"_meta,omitempty"`
}

// sessionExtensions is what session/new carries under kon's key of _meta.
type sessionExtensions struct {
	Instructions string `json:"instructions,omitempty"`
}

// Session is the answer to session/new, session/load, and session/resume.
type Session struct {
	SessionID     string         `json:"sessionId"`
	ConfigOptions []ConfigOption `json:"configOptions"`
}

type sessionParams struct {
	SessionID string `json:"sessionId"`
}

type promptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

type promptResponse struct {
	StopReason string `json:"stopReason"`
}

// ContentBlock is every kind of ACP content block in one struct; Type says
// which fields apply.
type ContentBlock struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	Data     string    `json:"data,omitempty"`
	MIMEType string    `json:"mimeType,omitempty"`
	URI      string    `json:"uri,omitempty"`
	Name     string    `json:"name,omitempty"`
	Resource *Resource `json:"resource,omitempty"`
}

// Resource is an embedded resource: Text for a text resource, Blob (base64)
// for a binary one.
type Resource struct {
	URI      string  `json:"uri"`
	MIMEType string  `json:"mimeType,omitempty"`
	Text     *string `json:"text,omitempty"`
	Blob     string  `json:"blob,omitempty"`
}

// TextBlock is a text content block.
func TextBlock(text string) ContentBlock { return ContentBlock{Type: "text", Text: text} }

type setConfigRequest struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

type setConfigResponse struct {
	ConfigOptions []ConfigOption `json:"configOptions"`
}

// ConfigOption is a session setting kon offers, such as the model.
type ConfigOption struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Category     string         `json:"category"`
	CurrentValue string         `json:"currentValue"`
	Options      []ConfigChoice `json:"options"`
}

// ConfigChoice is one value a ConfigOption can take.
type ConfigChoice struct {
	Value string `json:"value"`
	Name  string `json:"name"`
}

// Config option IDs kon offers.
const (
	ConfigModel  = "model"
	ConfigEffort = "effort"
)

type steerRequest struct {
	SessionID string `json:"sessionId"`
	Text      string `json:"text"`
}

type jobsResponse struct {
	Jobs []Job `json:"jobs"`
}

// Job is one of a session's background jobs.
type Job struct {
	ID      int    `json:"id"`
	Command string `json:"command"`
	Running bool   `json:"running"`
	Exit    string `json:"exit,omitempty"`
	Output  string `json:"output"`
}

type killJobRequest struct {
	SessionID string `json:"sessionId"`
	ID        int    `json:"id"`
}

// Update is one session/update, decoded flat: Kind is its sessionUpdate and
// says which fields apply.
type Update struct {
	Kind string `json:"sessionUpdate"`
	// Content is the chunk of a *_message_chunk or agent_thought_chunk.
	Content ContentBlock `json:"-"`
	// ToolCallID, Title, and Status describe a tool_call or tool_call_update.
	ToolCallID string `json:"toolCallId"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	// Used and Size are a usage_update's context tokens, and Cost its spend.
	Used int64 `json:"used"`
	Size int64 `json:"size"`
	Cost *Cost `json:"cost"`
	// ConfigOptions is a config_option_update's new options.
	ConfigOptions []ConfigOption `json:"configOptions"`
}

// Cost is a session's spend so far.
type Cost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// UnmarshalJSON reads Content only for the chunks, as a tool call's content
// is a list of a different shape under the same key.
func (u *Update) UnmarshalJSON(data []byte) error {
	type plain Update
	var fields struct {
		plain
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*u = Update(fields.plain)
	switch u.Kind {
	case "agent_message_chunk", "agent_thought_chunk", "user_message_chunk":
		if len(fields.Content) > 0 {
			return json.Unmarshal(fields.Content, &u.Content)
		}
	}
	return nil
}

type sessionNotification struct {
	SessionID string `json:"sessionId"`
	Update    Update `json:"update"`
}

type turnEnd struct {
	SessionID  string `json:"sessionId"`
	StopReason string `json:"stopReason"`
	Error      string `json:"error"`
}
