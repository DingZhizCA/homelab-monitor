/*
Package plugins tests explicit collection policy identity, cadence, and config bounds.
*/
package plugins

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCollectionPolicyRequiresExplicitBoundedRuntimeSettings(t *testing.T) {
	valid := CollectionPolicy{DeviceID: "node-a", PluginID: "system", CollectorID: "health", Revision: 1, PluginRevision: 3, Enabled: true, IntervalSeconds: 60, TimeoutMilliseconds: 5000, Config: json.RawMessage(`{}`)}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	for _, mutate := range []func(*CollectionPolicy){
		func(p *CollectionPolicy) { p.IntervalSeconds = 0 },
		func(p *CollectionPolicy) { p.TimeoutMilliseconds = 60001 },
		func(p *CollectionPolicy) { p.Config = json.RawMessage(`{`) },
		func(p *CollectionPolicy) { p.Config = bytes.Repeat([]byte{' '}, MaxCollectionConfigBytes+1) },
	} {
		candidate := valid
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatalf("invalid policy accepted: %+v", candidate)
		}
	}
	disabled := CollectionPolicy{DeviceID: "node-a", PluginID: "system", CollectorID: "health", Revision: 2}
	if err := disabled.Validate(); err != nil {
		t.Fatalf("explicit disable rejected: %v", err)
	}
	disabled.Config = json.RawMessage(`{}`)
	if err := disabled.Validate(); err == nil {
		t.Fatal("disabled policy retained runtime config")
	}
}
