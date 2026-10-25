/*
Package dashboard verifies authorization and resource bounds for metric history.

History fixtures are in memory and do not select a persistence or aggregation
strategy.
*/
package dashboard

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

type historySourceFake struct {
	points []HistoricalPoint
	err    error
	calls  int
	last   HistoryRequest
}

func (f *historySourceFake) QueryHistory(_ context.Context, request HistoryRequest) ([]HistoricalPoint, error) {
	f.calls++
	f.last = request
	return append([]HistoricalPoint(nil), f.points...), f.err
}

func TestHistoryQueryAuthorizesBeforeReadAndAppliesExplicitBudgets(t *testing.T) {
	start := time.Date(2026, 10, 18, 10, 0, 0, 0, time.FixedZone("test", 8*60*60))
	source := &historySourceFake{points: []HistoricalPoint{
		{DeviceID: "node-a", DescriptorID: "network.rx_bytes_per_second", SampledAt: start.Add(2 * time.Minute), Quality: "valid", Dimensions: map[string]string{"interface": "eth1"}},
		{DeviceID: "node-a", DescriptorID: "network.rx_bytes_per_second", SampledAt: start.Add(time.Minute), Quality: "partial", Dimensions: map[string]string{"interface": "eth0"}},
	}}
	actor := auth.Principal{UserID: "viewer-1", Role: auth.RoleViewer}
	service, err := NewHistoryService(source, metricPolicyFake{err: auth.ErrForbidden}, NewMetricRegistry(), HistoryBudget{MaxRange: time.Hour, MaxPoints: 10})
	if err != nil {
		t.Fatal(err)
	}
	request := HistoryRequest{DescriptorID: "network.rx_bytes_per_second", DeviceID: "node-a", From: start, To: start.Add(10 * time.Minute), Limit: 5}
	if _, err := service.Query(context.Background(), actor, request); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("unauthorized history query error=%v", err)
	}
	if source.calls != 0 {
		t.Fatal("unauthorized history query read storage")
	}

	service, err = NewHistoryService(source, metricPolicyFake{}, NewMetricRegistry(), HistoryBudget{MaxRange: time.Hour, MaxPoints: 10})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Query(context.Background(), actor, request)
	if err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || source.last.Limit != request.Limit || source.last.From.Location() != time.UTC {
		t.Fatalf("source request was not bounded and normalized: calls=%d request=%+v", source.calls, source.last)
	}
	if len(result.Points) != 2 || !result.Points[0].SampledAt.Before(result.Points[1].SampledAt) {
		t.Fatalf("history points were not sorted chronologically: %+v", result.Points)
	}
	result.Points[0].Dimensions["interface"] = "mutated"
	if source.points[1].Dimensions["interface"] != "eth0" {
		t.Fatal("caller mutation changed source-owned dimensions")
	}
}

func TestHistoryQueryRejectsInvalidRangesLimitsAndSourceResults(t *testing.T) {
	start := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	actor := auth.Principal{UserID: "owner-1", Role: auth.RoleOwner}
	source := &historySourceFake{}
	service, err := NewHistoryService(source, metricPolicyFake{}, NewMetricRegistry(), HistoryBudget{MaxRange: time.Hour, MaxPoints: 2})
	if err != nil {
		t.Fatal(err)
	}
	base := HistoryRequest{DescriptorID: "cpu.usage_percent", DeviceID: "node-a", From: start, To: start.Add(time.Minute), Limit: 1}
	for name, mutate := range map[string]func(*HistoryRequest){
		"empty range":         func(r *HistoryRequest) { r.To = r.From },
		"too wide":            func(r *HistoryRequest) { r.To = r.From.Add(2 * time.Hour) },
		"untrimmed device ID": func(r *HistoryRequest) { r.DeviceID = " node-a " },
		"zero limit":          func(r *HistoryRequest) { r.Limit = 0 },
		"over budget":         func(r *HistoryRequest) { r.Limit = 3 },
	} {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if _, err := service.Query(context.Background(), actor, request); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("invalid request error=%v", err)
			}
		})
	}
	if source.calls != 0 {
		t.Fatalf("invalid requests reached history source %d times", source.calls)
	}

	source.points = []HistoricalPoint{
		{DeviceID: "node-a", DescriptorID: "cpu.usage_percent", SampledAt: start, Quality: "valid"},
		{DeviceID: "node-a", DescriptorID: "cpu.usage_percent", SampledAt: start.Add(time.Second), Quality: "valid"},
	}
	if _, err := service.Query(context.Background(), actor, base); !errors.Is(err, ErrHistorySourceLimit) {
		t.Fatalf("over-limit source response error=%v", err)
	}
	source.points = []HistoricalPoint{{DeviceID: "node-b", DescriptorID: "cpu.usage_percent", SampledAt: start, Quality: "valid"}}
	if _, err := service.Query(context.Background(), actor, base); !errors.Is(err, ErrInvalidHistoryPoint) {
		t.Fatalf("out-of-scope source response error=%v", err)
	}
	invalidPoints := []struct {
		name  string
		point HistoricalPoint
	}{
		{"wrong descriptor", HistoricalPoint{DeviceID: "node-a", DescriptorID: "memory.usage_percent", SampledAt: start, Quality: "valid"}},
		{"outside interval", HistoricalPoint{DeviceID: "node-a", DescriptorID: "cpu.usage_percent", SampledAt: start.Add(time.Minute), Quality: "valid"}},
		{"unsupported quality", HistoricalPoint{DeviceID: "node-a", DescriptorID: "cpu.usage_percent", SampledAt: start, Quality: "unknown"}},
		{"unexpected dimension", HistoricalPoint{DeviceID: "node-a", DescriptorID: "cpu.usage_percent", SampledAt: start, Quality: "valid", Dimensions: map[string]string{"interface": "eth0"}}},
		{"non-finite value", HistoricalPoint{DeviceID: "node-a", DescriptorID: "cpu.usage_percent", SampledAt: start, Value: floatPointer(math.NaN()), Quality: "valid"}},
		{"invalid UTF-8 dimension", HistoricalPoint{DeviceID: "node-a", DescriptorID: "network.rx_bytes_per_second", SampledAt: start, Quality: "valid", Dimensions: map[string]string{"interface": string([]byte{0xff})}}},
		{"raw with resolution", HistoricalPoint{DeviceID: "node-a", DescriptorID: "cpu.usage_percent", SampledAt: start, Quality: "valid", ResolutionSeconds: 60, AggregationKind: "raw"}},
		{"aggregate without resolution", HistoricalPoint{DeviceID: "node-a", DescriptorID: "cpu.usage_percent", SampledAt: start, Quality: "estimated", AggregationKind: "gauge_mean"}},
	}
	for _, test := range invalidPoints {
		t.Run(test.name, func(t *testing.T) {
			source.points = []HistoricalPoint{test.point}
			if _, err := service.Query(context.Background(), actor, base); !errors.Is(err, ErrInvalidHistoryPoint) {
				t.Fatalf("invalid history point error=%v", err)
			}
		})
	}
}

func floatPointer(value float64) *float64 { return &value }

func TestHistoryQueryPreservesPresentEmptyDimensionValues(t *testing.T) {
	start := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	source := &historySourceFake{points: []HistoricalPoint{{
		DeviceID: "node-a", DescriptorID: "network.rx_bytes_per_second", SampledAt: start,
		Quality: "valid", Dimensions: map[string]string{"interface": ""},
	}}}
	service, err := NewHistoryService(source, metricPolicyFake{}, NewMetricRegistry(), HistoryBudget{MaxRange: time.Hour, MaxPoints: 1})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Query(context.Background(), auth.Principal{UserID: "viewer-1", Role: auth.RoleViewer}, HistoryRequest{
		DescriptorID: "network.rx_bytes_per_second", DeviceID: "node-a", From: start,
		To: start.Add(time.Minute), Limit: 1,
	})
	if err != nil {
		t.Fatalf("present empty dimension value was rejected: %v", err)
	}
	if value, exists := result.Points[0].Dimensions["interface"]; !exists || value != "" {
		t.Fatalf("empty dimension value was not preserved: %#v", result.Points[0].Dimensions)
	}
}

func TestHistoryQueryDetachesReturnedDataFromHistorySource(t *testing.T) {
	start := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	value := 12.5
	points := []HistoricalPoint{{
		DeviceID: "node-a", DescriptorID: "network.rx_bytes_per_second", SampledAt: start,
		Value: &value, Quality: "valid", Dimensions: map[string]string{"interface": "eth0"},
	}}
	source := borrowedHistorySource{points: points}
	service, err := NewHistoryService(source, metricPolicyFake{}, NewMetricRegistry(), HistoryBudget{MaxRange: time.Hour, MaxPoints: 1})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Query(context.Background(), auth.Principal{UserID: "viewer-1", Role: auth.RoleViewer}, HistoryRequest{
		DescriptorID: "network.rx_bytes_per_second", DeviceID: "node-a", From: start,
		To: start.Add(time.Minute), Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	*result.Points[0].Value = 99
	result.Points[0].Dimensions["interface"] = "mutated"
	if value != 12.5 || points[0].Dimensions["interface"] != "eth0" {
		t.Fatalf("caller changed history source data: value=%v dimensions=%v", value, points[0].Dimensions)
	}
}

type borrowedHistorySource struct{ points []HistoricalPoint }

func (s borrowedHistorySource) QueryHistory(context.Context, HistoryRequest) ([]HistoricalPoint, error) {
	return s.points, nil
}
