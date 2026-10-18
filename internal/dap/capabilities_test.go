package dap

import (
	"encoding/json"
	"testing"
)

func TestDecodeCapabilities(t *testing.T) {
	body := json.RawMessage(`{
		"supportsConfigurationDoneRequest": true,
		"supportsConditionalBreakpoints": false,
		"supportsTerminateRequest": true,
		"unknownCapability": true,
		"nonBoolean": "yes"
	}`)
	capabilities, raw, err := decodeCapabilities(body)
	if err != nil {
		t.Fatalf("decodeCapabilities() error = %v", err)
	}
	if !capabilities.SupportsConfigurationDoneRequest {
		t.Error("supportsConfigurationDoneRequest = false")
	}
	if capabilities.SupportsConditionalBreakpoints {
		t.Error("supportsConditionalBreakpoints = true")
	}
	if !capabilities.SupportsTerminateRequest {
		t.Error("supportsTerminateRequest = false")
	}
	if !raw["unknownCapability"] {
		t.Error("unknownCapability is not available in the raw map")
	}
	if _, ok := raw["nonBoolean"]; ok {
		t.Error("non-boolean capability was inserted in the raw map")
	}
	if raw["supportsConditionalBreakpoints"] {
		t.Error("false capability is true in the raw map")
	}
}

func TestDecodeCapabilitiesEmpty(t *testing.T) {
	for _, body := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage("{}")} {
		capabilities, raw, err := decodeCapabilities(body)
		if err != nil {
			t.Fatalf("decodeCapabilities(%q) error = %v", body, err)
		}
		if capabilities.SupportsConfigurationDoneRequest {
			t.Error("empty body enabled a capability")
		}
		if len(raw) != 0 {
			t.Errorf("raw = %v, want empty", raw)
		}
	}
}

func TestDecodeCapabilitiesRejectsInvalidShapes(t *testing.T) {
	tests := []json.RawMessage{
		json.RawMessage(`[]`),
		json.RawMessage(`"capabilities"`),
		json.RawMessage(`{"supportsConfigurationDoneRequest": {"nested": true}}`),
	}
	for _, body := range tests {
		if _, _, err := decodeCapabilities(body); err == nil {
			t.Errorf("decodeCapabilities(%s) error = nil", body)
		}
	}
}
