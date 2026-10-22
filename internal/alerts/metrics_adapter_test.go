/*
Package alerts tests conversion from query results without storage side effects.
*/
package alerts

import (
	"context"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

type alertSnapshotSource struct{ snapshot dashboard.Snapshot }

func (s alertSnapshotSource) Current(context.Context, string) (dashboard.Snapshot, bool, error) {
	return s.snapshot, true, nil
}

type allowMetricQuery struct{}

func (allowMetricQuery) AuthorizeMetric(auth.Principal, string, string) error { return nil }

func TestSamplesFromMetricResultsPreservesDimensionsAndQuality(t *testing.T) {
	rx := 120.0
	tx := 8.0
	results := []dashboard.MetricResult{{
		Descriptor: dashboard.MetricDescriptor{ID: "network.rx_bytes_per_second", Unit: "bytes_per_second", Dimensions: []string{"interface"}},
		DeviceID:   "node-a",
		Points: []dashboard.MetricPoint{
			{Value: &rx, Quality: "fresh", Dimensions: map[string]string{"interface": "eth0"}},
			{Value: &tx, Quality: "stale", Dimensions: map[string]string{"interface": "eth1"}},
		},
	}}
	samples, err := SamplesFromMetricResults(results)
	if err != nil {
		t.Fatal(err)
	}
	eth0 := samples[SampleKey("network.rx_bytes_per_second", map[string]string{"interface": "eth0"})]
	if eth0.Value != rx || eth0.Quality != QualityFresh || eth0.Unit != "bytes_per_second" || eth0.DeviceID != "node-a" {
		t.Fatalf("fresh sample=%+v", eth0)
	}
	eth1 := samples[SampleKey("network.rx_bytes_per_second", map[string]string{"interface": "eth1"})]
	if eth1.Value != tx || eth1.Quality != QualityStale {
		t.Fatalf("stale sample=%+v", eth1)
	}
	results[0].Points[0].Dimensions["interface"] = "mutated"
	if eth0.Labels["interface"] != "eth0" {
		t.Fatal("adapter output shares mutable dimension labels with query results")
	}
}

func TestSamplesFromMetricResultsPreservesMissingAndUnknownQualityAsUnknown(t *testing.T) {
	value := 0.0
	samples, err := SamplesFromMetricResults([]dashboard.MetricResult{
		{
			Descriptor: dashboard.MetricDescriptor{ID: "cpu.temperature_celsius", Unit: "celsius"},
			DeviceID:   "node-a",
			Points:     []dashboard.MetricPoint{{Quality: "missing"}},
		},
		{
			Descriptor: dashboard.MetricDescriptor{ID: "system.load1", Unit: "load"},
			DeviceID:   "node-a",
			Points:     []dashboard.MetricPoint{{Value: &value, Quality: "unrecognized"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if samples["cpu.temperature_celsius"].Quality != QualityMissing {
		t.Fatalf("missing sample quality=%q", samples["cpu.temperature_celsius"].Quality)
	}
	if samples[SampleKey("cpu.temperature_celsius", nil)].Quality != QualityMissing {
		t.Fatal("dimensionless missing sample did not retain descriptor key")
	}
	if samples["system.load1"].Quality != QualityUnsupported {
		t.Fatalf("unrecognized quality=%q", samples["system.load1"].Quality)
	}
}

func TestSamplesFromMetricResultsRejectsAmbiguousOrInvalidResults(t *testing.T) {
	value := 1.0
	base := dashboard.MetricResult{
		Descriptor: dashboard.MetricDescriptor{ID: "system.load1", Unit: "load"},
		DeviceID:   "node-a",
		Points:     []dashboard.MetricPoint{{Value: &value, Quality: "fresh"}},
	}
	if _, err := SamplesFromMetricResults([]dashboard.MetricResult{base, base}); err == nil {
		t.Fatal("duplicate metric series were silently overwritten")
	}
	invalid := base
	invalid.DeviceID = ""
	if _, err := SamplesFromMetricResults([]dashboard.MetricResult{invalid}); err == nil {
		t.Fatal("result without device identity was accepted")
	}
}

func TestDashboardQueryResultsFeedDimensionSelectedAlertEvaluation(t *testing.T) {
	now := time.Now().UTC()
	source := alertSnapshotSource{snapshot: dashboard.Snapshot{
		Envelope: metrics.MetricEnvelope{
			Device: metrics.DeviceInfo{ID: "node-a"}, Timestamp: now,
			Networks: []metrics.NetworkMetrics{
				{Interface: "eth0", RXBytesPerSecond: 150},
				{Interface: "eth1", RXBytesPerSecond: 25},
			},
		},
		ReceivedAt: now, Status: "online",
	}}
	registry := dashboard.NewMetricRegistry()
	query, err := dashboard.NewQueryService(source, allowMetricQuery{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	actor := auth.Principal{UserID: "owner-1", Role: auth.RoleOwner}
	result, err := query.QueryLatest(context.Background(), actor, "network.rx_bytes_per_second", "node-a")
	if err != nil {
		t.Fatal(err)
	}
	samples, err := SamplesFromMetricResults([]dashboard.MetricResult{result})
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := NewEvaluator(registry, Limits{MaxDepth: 2, MaxConditions: 2})
	if err != nil {
		t.Fatal(err)
	}
	rule := Rule{
		ID: "eth0-rx", DeviceID: "node-a", Revision: 1, Severity: SeverityWarning, Enabled: true,
		Expression: Expression{Metric: &MetricComparison{
			DescriptorID: "network.rx_bytes_per_second", Labels: map[string]string{"interface": "eth0"},
			Operator: OperatorGreaterThan, Threshold: 100, Unit: "bytes_per_second",
		}},
	}
	evaluation, err := evaluator.Evaluate(context.Background(), rule, samples)
	if err != nil || evaluation.State != StateTrue {
		t.Fatalf("dashboard metric alert evaluation=%+v err=%v", evaluation, err)
	}
}

func TestCurrentSnapshotSamplesPreservesSnapshotLivenessWithoutCollection(t *testing.T) {
	now := time.Now().UTC()
	source := alertSnapshotSource{snapshot: dashboard.Snapshot{
		Envelope:   metrics.MetricEnvelope{Device: metrics.DeviceInfo{ID: "node-a"}, Timestamp: now, CPU: metrics.CPUMetrics{UsagePercent: 42}},
		ReceivedAt: now, Status: "offline",
	}}
	samplesSource, err := NewCurrentSnapshotSamples(source, dashboard.NewMetricRegistry())
	if err != nil {
		t.Fatal(err)
	}
	samples, err := samplesSource.Samples(context.Background(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if sample := samples["cpu.usage_percent"]; sample.Value != 42 || sample.Quality != QualityStale {
		t.Fatalf("CPU sample=%+v", sample)
	}
	if sample := samples["cpu.temperature_celsius"]; sample.Quality != QualityMissing {
		t.Fatalf("optional sample=%+v", sample)
	}
}
