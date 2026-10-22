/*
Package metrics tests the bounded identity and ordering rules for sample batches.
*/
package metrics

import (
	"regexp"
	"testing"
	"time"
)

func TestNewBatchIDReturnsValidDistinctRetryKey(t *testing.T) {
	first, err := NewBatchID()
	if err != nil {
		t.Fatalf("generate first batch ID: %v", err)
	}
	second, err := NewBatchID()
	if err != nil {
		t.Fatalf("generate second batch ID: %v", err)
	}
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(first) {
		t.Fatalf("batch ID %q is not a 128-bit lowercase hex value", first)
	}
	if first == second {
		t.Fatal("independent batch ID generations unexpectedly collided")
	}
}

func TestBatchRequiresOneDeviceAndStrictTimestampOrder(t *testing.T) {
	first := validEnvelope()
	first.Timestamp = time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)
	second := first
	second.Timestamp = first.Timestamp.Add(time.Minute)
	batch := Batch{ID: "upload-01", DeviceID: first.Device.ID, Snapshots: []MetricEnvelope{first, second}}
	if err := batch.Validate(2); err != nil {
		t.Fatalf("valid batch rejected: %v", err)
	}

	invalid := []Batch{
		{ID: "upload-01", DeviceID: first.Device.ID, Snapshots: []MetricEnvelope{first, second}},
		{ID: "upload-01", DeviceID: "other-device", Snapshots: []MetricEnvelope{first}},
		{ID: "upload-01", DeviceID: first.Device.ID, Snapshots: []MetricEnvelope{second, first}},
		{ID: "upload-01", DeviceID: first.Device.ID, Snapshots: []MetricEnvelope{first, first}},
		{ID: " bad id ", DeviceID: first.Device.ID, Snapshots: []MetricEnvelope{first}},
		{ID: "upload-01", DeviceID: first.Device.ID},
	}
	for index, candidate := range invalid {
		if err := candidate.Validate(1); err == nil {
			t.Errorf("invalid batch %d was accepted", index)
		}
	}
}
