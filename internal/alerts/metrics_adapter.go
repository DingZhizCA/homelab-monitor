/*
Package alerts adapts authorized Dashboard metric results to rule samples.

The adapter preserves descriptor dimensions and degraded quality states. It
does not query storage, authorize users, or initiate collection.
*/
package alerts

import (
	"errors"
	"fmt"
	"math"

	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
)

// SamplesFromMetricResults converts one or more query results into evaluator input.
func SamplesFromMetricResults(results []dashboard.MetricResult) (Samples, error) {
	samples := make(Samples)
	for _, result := range results {
		if result.Descriptor.ID == "" || result.DeviceID == "" {
			return nil, errors.New("metric result requires a descriptor and device ID")
		}
		for _, point := range result.Points {
			quality := alertQuality(point.Quality)
			if point.Value == nil {
				quality = QualityMissing
			} else if (result.Status == "stale" || result.Status == "offline") && quality == QualityFresh {
				quality = QualityStale
			}
			value := 0.0
			if point.Value != nil {
				value = *point.Value
				if math.IsNaN(value) || math.IsInf(value, 0) {
					quality = QualityUnsupported
				}
			}
			labels := cloneAlertLabels(point.Dimensions)
			key := SampleKey(result.Descriptor.ID, labels)
			if _, exists := samples[key]; exists {
				return nil, fmt.Errorf("metric result repeats descriptor %q and dimensions %v", result.Descriptor.ID, labels)
			}
			samples[key] = MetricSample{
				DeviceID: result.DeviceID,
				Value:    value,
				Unit:     result.Descriptor.Unit,
				Quality:  quality,
				Labels:   labels,
			}
		}
	}
	return samples, nil
}

func alertQuality(quality string) Quality {
	switch quality {
	case "fresh":
		return QualityFresh
	case "stale":
		return QualityStale
	case "missing":
		return QualityMissing
	default:
		return QualityUnsupported
	}
}

func cloneAlertLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	cloned := make(map[string]string, len(labels))
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}
