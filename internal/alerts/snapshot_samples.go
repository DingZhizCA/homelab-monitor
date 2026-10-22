/*
Package alerts adapts Server current snapshots to evaluation samples.

The source reads every bounded built-in descriptor from an existing snapshot.
It never schedules collection or queries historical storage.
*/
package alerts

import (
	"context"
	"errors"
	"fmt"

	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
)

// CurrentSnapshotSamples reads evaluator input from the latest Server snapshot.
type CurrentSnapshotSamples struct {
	source   dashboard.SnapshotSource
	registry *dashboard.MetricRegistry
}

// NewCurrentSnapshotSamples binds a snapshot source to the approved metric registry.
func NewCurrentSnapshotSamples(source dashboard.SnapshotSource, registry *dashboard.MetricRegistry) (*CurrentSnapshotSamples, error) {
	if source == nil || registry == nil {
		return nil, errors.New("snapshot source and metric registry are required")
	}
	return &CurrentSnapshotSamples{source: source, registry: registry}, nil
}

// Samples returns all approved current values for one device.
func (s *CurrentSnapshotSamples) Samples(ctx context.Context, deviceID string) (Samples, error) {
	snapshot, found, err := s.source.Current(ctx, deviceID)
	if err != nil {
		return nil, fmt.Errorf("read current metric snapshot: %w", err)
	}
	if !found {
		return nil, dashboard.ErrSnapshotUnavailable
	}
	if snapshot.Envelope.Device.ID != deviceID {
		return nil, dashboard.ErrInvalidMetricSnapshot
	}
	descriptors := s.registry.List()
	results := make([]dashboard.MetricResult, 0, len(descriptors))
	for _, descriptor := range descriptors {
		points, err := s.registry.Extract(descriptor.ID, snapshot.Envelope)
		if err != nil {
			return nil, fmt.Errorf("extract %s: %w", descriptor.ID, err)
		}
		for index := range points {
			switch {
			case points[index].Value == nil:
				points[index].Quality = "missing"
			case snapshot.Status == "stale" || snapshot.Status == "offline":
				points[index].Quality = "stale"
			default:
				points[index].Quality = "fresh"
			}
		}
		if len(points) == 0 {
			points = []dashboard.MetricPoint{{Quality: "missing"}}
		}
		results = append(results, dashboard.MetricResult{
			Descriptor: descriptor, DeviceID: deviceID,
			SampledAt: snapshot.Envelope.Timestamp.UTC(), ReceivedAt: snapshot.ReceivedAt.UTC(),
			Status: snapshot.Status, Points: points,
		})
	}
	return SamplesFromMetricResults(results)
}
