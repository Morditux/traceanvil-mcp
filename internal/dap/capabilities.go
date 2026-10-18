package dap

import (
	"bytes"
	"encoding/json"
)

// Capabilities is the typed subset of the DAP initialize response that the
// debugger backends use. Unknown capabilities remain available through
// Client.Supports, so a newer adapter cannot break the client.
type Capabilities struct {
	SupportsConfigurationDoneRequest      bool `json:"supportsConfigurationDoneRequest"`
	SupportsFunctionBreakpoints           bool `json:"supportsFunctionBreakpoints"`
	SupportsConditionalBreakpoints        bool `json:"supportsConditionalBreakpoints"`
	SupportsHitConditionalBreakpoints     bool `json:"supportsHitConditionalBreakpoints"`
	SupportsEvaluateForHovers             bool `json:"supportsEvaluateForHovers"`
	SupportsStepBack                      bool `json:"supportsStepBack"`
	SupportsSetVariable                   bool `json:"supportsSetVariable"`
	SupportsRestartFrame                  bool `json:"supportsRestartFrame"`
	SupportsGotoTargetsRequest            bool `json:"supportsGotoTargetsRequest"`
	SupportsStepInTargetsRequest          bool `json:"supportsStepInTargetsRequest"`
	SupportsCompletionsRequest            bool `json:"supportsCompletionsRequest"`
	SupportsModulesRequest                bool `json:"supportsModulesRequest"`
	SupportsRestartRequest                bool `json:"supportsRestartRequest"`
	SupportsExceptionOptions              bool `json:"supportsExceptionOptions"`
	SupportsValueFormattingOptions        bool `json:"supportsValueFormattingOptions"`
	SupportsExceptionInfoRequest          bool `json:"supportsExceptionInfoRequest"`
	SupportsTerminateRequest              bool `json:"supportsTerminateRequest"`
	SupportsDataBreakpoints               bool `json:"supportsDataBreakpoints"`
	SupportsReadMemoryRequest             bool `json:"supportsReadMemoryRequest"`
	SupportsWriteMemoryRequest            bool `json:"supportsWriteMemoryRequest"`
	SupportsDisassembleRequest            bool `json:"supportsDisassembleRequest"`
	SupportsSteppingGranularity           bool `json:"supportsSteppingGranularity"`
	SupportsInstructionBreakpoints        bool `json:"supportsInstructionBreakpoints"`
	SupportsDelayedStackTraceLoading      bool `json:"supportsDelayedStackTraceLoading"`
	SupportsLoadedSourcesRequest          bool `json:"supportsLoadedSourcesRequest"`
	SupportsLogPoints                     bool `json:"supportsLogPoints"`
	SupportsTerminateThreadsRequest       bool `json:"supportsTerminateThreadsRequest"`
	SupportsSetExpression                 bool `json:"supportsSetExpression"`
	SupportsTerminateDebuggee             bool `json:"supportTerminateDebuggee"`
	SupportsSuspendDebuggee               bool `json:"supportsSuspendDebuggee"`
	SupportsBreakpointLocationsRequest    bool `json:"supportsBreakpointLocationsRequest"`
	SupportsClipboardContext              bool `json:"supportsClipboardContext"`
	SupportsExceptionFilterOptions        bool `json:"supportsExceptionFilterOptions"`
	SupportsSingleThreadExecutionRequests bool `json:"supportsSingleThreadExecutionRequests"`
}

// decodeCapabilities decodes the initialize response body twice: into the typed
// subset and into a raw boolean map for forward-compatible Supports queries.
func decodeCapabilities(body json.RawMessage) (Capabilities, map[string]bool, error) {
	var (
		capabilities Capabilities
		values       map[string]json.RawMessage
	)
	if len(body) == 0 || bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		return capabilities, map[string]bool{}, nil
	}
	if err := json.Unmarshal(body, &capabilities); err != nil {
		return Capabilities{}, nil, protocolError("decode capabilities", err)
	}
	if err := json.Unmarshal(body, &values); err != nil {
		return Capabilities{}, nil, protocolError("decode capabilities map", err)
	}
	raw := make(map[string]bool, len(values))
	for name, value := range values {
		var enabled bool
		if err := json.Unmarshal(value, &enabled); err == nil {
			raw[name] = enabled
		}
	}
	return capabilities, raw, nil
}
