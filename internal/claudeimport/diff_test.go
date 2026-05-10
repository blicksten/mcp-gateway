package claudeimport

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDriftFields_NoExistingEntry(t *testing.T) {
	got := DriftFields(
		json.RawMessage(`{"type":"stdio","command":"x"}`),
		GatewayState{Entries: map[string]json.RawMessage{}},
		"absent")
	if got != nil {
		t.Errorf("DriftFields = %v, want nil", got)
	}
}

func TestDriftFields_IdenticalEntries(t *testing.T) {
	candidate := json.RawMessage(`{"type":"stdio","command":"/usr/bin/x","args":["a","b"]}`)
	gateway := GatewayState{
		Entries: map[string]json.RawMessage{
			"x": json.RawMessage(`{"type":"stdio","command":"/usr/bin/x","args":["a","b"]}`),
		},
	}
	got := DriftFields(candidate, gateway, "x")
	if len(got) != 0 {
		t.Errorf("expected no drift, got %v", got)
	}
}

func TestDriftFields_FieldOrder_DoesNotCauseFalseDrift(t *testing.T) {
	candidate := json.RawMessage(`{"type":"stdio","command":"/usr/bin/x","args":["a","b"]}`)
	gateway := GatewayState{
		Entries: map[string]json.RawMessage{
			"x": json.RawMessage(`{"args":["a","b"],"command":"/usr/bin/x","type":"stdio"}`),
		},
	}
	got := DriftFields(candidate, gateway, "x")
	if len(got) != 0 {
		t.Errorf("field-order should not be drift, got %v", got)
	}
}

func TestDriftFields_DifferingArgs(t *testing.T) {
	candidate := json.RawMessage(`{"type":"stdio","command":"/usr/bin/x","args":["a"]}`)
	gateway := GatewayState{
		Entries: map[string]json.RawMessage{
			"x": json.RawMessage(`{"type":"stdio","command":"/usr/bin/x","args":["a","b"]}`),
		},
	}
	got := DriftFields(candidate, gateway, "x")
	want := []string{"args"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DriftFields = %v, want %v", got, want)
	}
}

func TestDriftFields_KeyAddedOnOneSide(t *testing.T) {
	candidate := json.RawMessage(`{"type":"stdio","command":"x"}`)
	gateway := GatewayState{
		Entries: map[string]json.RawMessage{
			"x": json.RawMessage(`{"type":"stdio","command":"x","cwd":"/tmp"}`),
		},
	}
	got := DriftFields(candidate, gateway, "x")
	want := []string{"cwd"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DriftFields = %v, want %v", got, want)
	}
}

func TestDriftFields_MultipleDifferences_Sorted(t *testing.T) {
	candidate := json.RawMessage(`{"type":"http","url":"http://localhost:80","headers":{"X":"a"}}`)
	gateway := GatewayState{
		Entries: map[string]json.RawMessage{
			"x": json.RawMessage(`{"type":"http","url":"http://other:80","headers":{"X":"b"}}`),
		},
	}
	got := DriftFields(candidate, gateway, "x")
	want := []string{"headers", "url"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DriftFields = %v, want %v", got, want)
	}
}

func TestDriftFields_NonObjectCandidate(t *testing.T) {
	got := DriftFields(
		json.RawMessage(`null`),
		GatewayState{
			Entries: map[string]json.RawMessage{"x": json.RawMessage(`{"type":"stdio"}`)},
		},
		"x")
	if got != nil {
		t.Errorf("non-object candidate should yield no drift, got %v", got)
	}
}

func TestDriftFields_NestedObjectsCanonicalized(t *testing.T) {
	candidate := json.RawMessage(`{"env":{"A":"1","B":"2"}}`)
	gateway := GatewayState{
		Entries: map[string]json.RawMessage{
			"x": json.RawMessage(`{"env":{"B":"2","A":"1"}}`),
		},
	}
	got := DriftFields(candidate, gateway, "x")
	if len(got) != 0 {
		t.Errorf("nested key-order should not be drift, got %v", got)
	}
}
