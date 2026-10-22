/*
Package audit tests bounded construction of secret-free management events.
*/
package audit

import (
	"strings"
	"testing"
	"time"
)

func TestNewEventValidatesAndNormalizesTime(t *testing.T) {
	location := time.FixedZone("test", 8*60*60)
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, location)
	event, err := NewEvent(at, "owner-id", "auth.user.update", "user", "viewer-id")
	if err != nil {
		t.Fatal(err)
	}
	if event.OccurredAt.Location() != time.UTC || !event.OccurredAt.Equal(at) {
		t.Fatalf("event time=%v, want the same instant normalized to UTC", event.OccurredAt)
	}
	for _, invalid := range []Event{
		{},
		{OccurredAt: at, ActorID: " owner ", Action: "auth.user.update", ResourceType: "user", ResourceID: "viewer"},
		{OccurredAt: at, ActorID: "owner", Action: "Auth User Update", ResourceType: "user", ResourceID: "viewer"},
		{OccurredAt: at, ActorID: "owner", Action: "auth.user.update", ResourceType: "User", ResourceID: "viewer"},
		{OccurredAt: at, ActorID: "owner", Action: "auth.user.update", ResourceType: "user", ResourceID: strings.Repeat("x", maxIdentityBytes+1)},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid event was accepted: %+v", invalid)
		}
	}
}
