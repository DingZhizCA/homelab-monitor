/*
Package storage builds typed plugin metric aggregates from bounded raw scans.

Each affected bucket is recomputed in timestamp order, which preserves counter
reset and state transition semantics when queued observations arrive late.
*/
package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

type PluginAggregationConfig struct {
	Interval        time.Duration
	MaxObservations int
	MaxScanned      int
	RawRetention    time.Duration
	Timeout         time.Duration
}

type PluginAggregationObservation struct {
	ProcessedObservations int
	ScannedObservations   int
	UpdatedSeries         int
	PendingObservations   int64
	Duration              time.Duration
	Err                   error
}

type PluginAggregationObserver func(PluginAggregationObservation)

type PluginMetricAggregationManager struct {
	database *sql.DB
	config   PluginAggregationConfig
	observe  PluginAggregationObserver
	now      func() time.Time
	mu       sync.Mutex
}

func NewPluginMetricAggregationManager(database *sql.DB, config PluginAggregationConfig, observer PluginAggregationObserver) (*PluginMetricAggregationManager, error) {
	if database == nil || observer == nil || config.Interval <= 0 || config.MaxObservations < 1 || config.MaxScanned < 1 || config.RawRetention <= 0 || config.Timeout <= 0 {
		return nil, errors.New("metrics database, observer, and positive plugin aggregation bounds are required")
	}
	tx, err := database.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin plugin metric aggregation activation: %w", err)
	}
	defer tx.Rollback()
	var enabled int
	if err := tx.QueryRow(`SELECT enabled FROM plugin_metric_aggregation_control WHERE id = 1`).Scan(&enabled); err != nil {
		return nil, fmt.Errorf("read plugin metric aggregation activation: %w", err)
	}
	if _, err := tx.Exec(`UPDATE plugin_metric_aggregation_control SET enabled = 1 WHERE id = 1`); err != nil {
		return nil, fmt.Errorf("enable plugin metric aggregation queue: %w", err)
	}
	if enabled == 0 {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO plugin_metric_aggregation_pending (batch_id, sequence)
			SELECT batch_id, sequence FROM plugin_metric_observations ORDER BY rowid`); err != nil {
			return nil, fmt.Errorf("queue existing plugin metric observations: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit plugin metric aggregation activation: %w", err)
	}
	return &PluginMetricAggregationManager{database: database, config: config, observe: observer, now: time.Now}, nil
}

func (m *PluginMetricAggregationManager) Run(ctx context.Context) {
	m.Aggregate(ctx)
	ticker := time.NewTicker(m.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Aggregate(ctx)
		}
	}
}

func (m *PluginMetricAggregationManager) Aggregate(ctx context.Context) PluginAggregationObservation {
	m.mu.Lock()
	defer m.mu.Unlock()
	started := time.Now()
	result := PluginAggregationObservation{}
	opCtx, cancel := context.WithTimeout(ctx, m.config.Timeout)
	defer cancel()
	tx, err := m.database.BeginTx(opCtx, nil)
	if err != nil {
		result.Err = fmt.Errorf("begin plugin metric aggregation: %w", err)
		return m.finish(started, result)
	}
	defer tx.Rollback()
	pending, err := readPendingPluginObservations(opCtx, tx, m.config.MaxObservations)
	if err != nil {
		result.Err = err
		return m.finish(started, result)
	}
	targets := make(map[pluginAggregateTarget]*pluginAggregateWork)
	for _, item := range pending {
		var observation metrics.PluginObservation
		if err := json.Unmarshal(item.encoded, &observation); err != nil || observation.Validate() != nil || observation.DeviceID != item.deviceID || storageTimestamp(observation.ObservedAt) != item.observedAt {
			result.Err = errors.New("pending plugin metric observation violates its indexed envelope")
			return m.finish(started, result)
		}
		for _, sample := range observation.Samples {
			for _, resolution := range []int{minuteResolution, hourResolution, dayResolution} {
				target := pluginAggregateTarget{deviceID: observation.DeviceID, pluginID: observation.PluginID, pluginVersion: observation.PluginVersion, collectorID: observation.CollectorID, metricID: sample.MetricID, resolution: resolution, bucket: observation.ObservedAt.UTC().Truncate(time.Duration(resolution) * time.Second)}
				work := targets[target]
				if work == nil {
					work = &pluginAggregateWork{target: target}
					targets[target] = work
				}
				work.values = append(work.values, pluginIncomingValue{at: observation.ObservedAt.UTC(), sample: sample})
			}
		}
	}
	ordered := make([]*pluginAggregateWork, 0, len(targets))
	for _, work := range targets {
		ordered = append(ordered, work)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].target.key() < ordered[j].target.key() })
	cutoff := m.now().UTC().Add(-m.config.RawRetention)
	for _, work := range ordered {
		if work.target.bucket.Before(cutoff) {
			recompute, checkErr := oldPluginTargetNeedsRecompute(opCtx, tx, *work, cutoff)
			if checkErr != nil {
				result.Err = checkErr
				return m.finish(started, result)
			}
			if recompute {
				scanned, updated, err := recomputePluginAggregate(opCtx, tx, work.target, m.config.MaxScanned-result.ScannedObservations, "partial")
				result.ScannedObservations += scanned
				if err != nil {
					result.Err = err
					return m.finish(started, result)
				}
				result.UpdatedSeries += updated
				continue
			}
			updated, err := incrementPluginAggregate(opCtx, tx, *work)
			if err != nil {
				result.Err = err
				return m.finish(started, result)
			}
			result.UpdatedSeries += updated
			continue
		}
		scanned, updated, err := recomputePluginAggregate(opCtx, tx, work.target, m.config.MaxScanned-result.ScannedObservations, "estimated")
		result.ScannedObservations += scanned
		if err != nil {
			result.Err = err
			return m.finish(started, result)
		}
		result.UpdatedSeries += updated
	}
	for _, item := range pending {
		if _, err := tx.ExecContext(opCtx, `DELETE FROM plugin_metric_aggregation_pending WHERE id = ?`, item.id); err != nil {
			result.Err = fmt.Errorf("complete plugin metric aggregation: %w", err)
			return m.finish(started, result)
		}
		result.ProcessedObservations++
	}
	if err := tx.QueryRowContext(opCtx, `SELECT COUNT(*) FROM plugin_metric_aggregation_pending`).Scan(&result.PendingObservations); err != nil {
		result.Err = fmt.Errorf("count pending plugin metric aggregation: %w", err)
		return m.finish(started, result)
	}
	if err := tx.Commit(); err != nil {
		result.Err = fmt.Errorf("commit plugin metric aggregation: %w", err)
		result.ProcessedObservations, result.UpdatedSeries = 0, 0
	}
	return m.finish(started, result)
}

type pendingPluginObservation struct {
	id                   int64
	deviceID, observedAt string
	encoded              []byte
}

func readPendingPluginObservations(ctx context.Context, tx *sql.Tx, limit int) ([]pendingPluginObservation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT pending.id, observations.device_id, observations.observed_at, observations.observation_json
		FROM plugin_metric_aggregation_pending AS pending
		JOIN plugin_metric_observations AS observations ON observations.batch_id = pending.batch_id AND observations.sequence = pending.sequence
		ORDER BY pending.id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read pending plugin metric aggregation: %w", err)
	}
	defer rows.Close()
	var result []pendingPluginObservation
	for rows.Next() {
		var item pendingPluginObservation
		if err := rows.Scan(&item.id, &item.deviceID, &item.observedAt, &item.encoded); err != nil {
			return nil, fmt.Errorf("scan pending plugin metric aggregation: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type pluginAggregateTarget struct {
	deviceID, pluginID, pluginVersion, collectorID, metricID string
	resolution                                               int
	bucket                                                   time.Time
}

type pluginIncomingValue struct {
	at     time.Time
	sample metrics.PluginMetricObservation
}

type pluginAggregateWork struct {
	target pluginAggregateTarget
	values []pluginIncomingValue
}

func (t pluginAggregateTarget) key() string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%010d\x00%s", t.deviceID, t.pluginID, t.pluginVersion, t.collectorID, t.metricID, t.resolution, storageTimestamp(t.bucket))
}

type aggregateSeries struct {
	typ, unit, labels string
	values            []aggregateValue
}
type aggregateValue struct {
	at      time.Time
	value   json.RawMessage
	quality string
}

func recomputePluginAggregate(ctx context.Context, tx *sql.Tx, target pluginAggregateTarget, remaining int, quality string) (int, int, error) {
	if remaining < 1 {
		return 0, 0, errors.New("plugin metric aggregation scan limit was reached")
	}
	end := target.bucket.Add(time.Duration(target.resolution) * time.Second)
	rows, err := tx.QueryContext(ctx, `SELECT observation_json FROM plugin_metric_observations
		WHERE device_id = ? AND plugin_id = ? AND plugin_version = ? AND collector_id = ? AND observed_at >= ? AND observed_at < ?
		ORDER BY observed_at, batch_id, sequence LIMIT ?`, target.deviceID, target.pluginID, target.pluginVersion, target.collectorID, storageTimestamp(target.bucket), storageTimestamp(end), remaining+1)
	if err != nil {
		return 0, 0, fmt.Errorf("scan plugin aggregate source: %w", err)
	}
	defer rows.Close()
	series := make(map[string]*aggregateSeries)
	scanned := 0
	for rows.Next() {
		scanned++
		if scanned > remaining {
			return scanned, 0, errors.New("plugin metric aggregation scan limit was reached")
		}
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return scanned, 0, fmt.Errorf("scan plugin aggregate observation: %w", err)
		}
		var observation metrics.PluginObservation
		if err := json.Unmarshal(encoded, &observation); err != nil || observation.Validate() != nil ||
			observation.DeviceID != target.deviceID || observation.PluginID != target.pluginID || observation.PluginVersion != target.pluginVersion || observation.CollectorID != target.collectorID {
			return scanned, 0, errors.New("stored plugin metric observation is invalid")
		}
		for _, sample := range observation.Samples {
			if sample.MetricID != target.metricID {
				continue
			}
			labels, _ := json.Marshal(nonNilDimensions(sample.Labels))
			key := sample.Type + "\x00" + sample.Unit + "\x00" + string(labels)
			item := series[key]
			if item == nil {
				item = &aggregateSeries{typ: sample.Type, unit: sample.Unit, labels: string(labels)}
				series[key] = item
			}
			item.values = append(item.values, aggregateValue{at: observation.ObservedAt.UTC(), value: append(json.RawMessage(nil), sample.Value...), quality: sample.Quality})
		}
	}
	if err := rows.Err(); err != nil {
		return scanned, 0, fmt.Errorf("iterate plugin aggregate source: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_metric_aggregates WHERE resolution_seconds = ? AND bucket_start = ? AND device_id = ? AND plugin_id = ? AND plugin_version = ? AND collector_id = ? AND metric_id = ?`, target.resolution, storageTimestamp(target.bucket), target.deviceID, target.pluginID, target.pluginVersion, target.collectorID, target.metricID); err != nil {
		return scanned, 0, fmt.Errorf("replace plugin metric aggregate: %w", err)
	}
	updated := 0
	for _, item := range series {
		summary, kind, count, last, err := summarizePluginSeries(item.typ, item.values)
		if err != nil {
			return scanned, updated, fmt.Errorf("summarize plugin metric %s: %w", target.metricID, err)
		}
		if count == 0 {
			continue
		}
		first := last
		for _, value := range item.values {
			if value.quality != "missing" && value.quality != "stale" && value.at.Before(first) {
				first = value.at
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_metric_aggregates
			(resolution_seconds, bucket_start, device_id, plugin_id, plugin_version, collector_id, metric_id, metric_type, unit, labels_json, aggregation_kind, summary_json, quality, sample_count, first_observed_at, last_observed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, target.resolution, storageTimestamp(target.bucket), target.deviceID, target.pluginID, target.pluginVersion, target.collectorID, target.metricID, item.typ, item.unit, item.labels, kind, summary, quality, count, storageTimestamp(first), storageTimestamp(last)); err != nil {
			return scanned, updated, fmt.Errorf("insert plugin metric aggregate: %w", err)
		}
		updated++
	}
	return scanned, updated, nil
}

func oldPluginTargetNeedsRecompute(ctx context.Context, tx *sql.Tx, work pluginAggregateWork, cutoff time.Time) (bool, error) {
	var firstTimestamp, lastTimestamp sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT MIN(first_observed_at), MAX(last_observed_at) FROM plugin_metric_aggregates
		WHERE resolution_seconds = ? AND bucket_start = ? AND device_id = ? AND plugin_id = ? AND plugin_version = ? AND collector_id = ? AND metric_id = ?`, work.target.resolution, storageTimestamp(work.target.bucket), work.target.deviceID, work.target.pluginID, work.target.pluginVersion, work.target.collectorID, work.target.metricID).Scan(&firstTimestamp, &lastTimestamp)
	if err != nil {
		return false, fmt.Errorf("read plugin metric aggregate bounds: %w", err)
	}
	if !firstTimestamp.Valid || !lastTimestamp.Valid {
		return false, nil
	}
	first, err := time.Parse(storageTimeLayout, firstTimestamp.String)
	if err != nil {
		return false, errors.New("stored plugin metric aggregate bounds are invalid")
	}
	last, err := time.Parse(storageTimeLayout, lastTimestamp.String)
	if err != nil {
		return false, errors.New("stored plugin metric aggregate bounds are invalid")
	}
	for _, incoming := range work.values {
		if incoming.at.Before(last) {
			if first.Before(cutoff) {
				return false, errors.New("plugin metric observation is older than the exact aggregation window")
			}
			return true, nil
		}
	}
	return false, nil
}

func incrementPluginAggregate(ctx context.Context, tx *sql.Tx, work pluginAggregateWork) (int, error) {
	sort.SliceStable(work.values, func(i, j int) bool { return work.values[i].at.Before(work.values[j].at) })
	updated := 0
	for _, incoming := range work.values {
		if incoming.sample.Quality == "missing" || incoming.sample.Quality == "stale" {
			continue
		}
		labels, _ := json.Marshal(nonNilDimensions(incoming.sample.Labels))
		var kind, quality, lastTimestamp string
		var summary []byte
		var count int
		err := tx.QueryRowContext(ctx, `SELECT aggregation_kind, summary_json, quality, sample_count, last_observed_at
			FROM plugin_metric_aggregates WHERE resolution_seconds = ? AND bucket_start = ? AND device_id = ? AND plugin_id = ? AND plugin_version = ? AND collector_id = ? AND metric_id = ? AND metric_type = ? AND unit = ? AND labels_json = ?`,
			work.target.resolution, storageTimestamp(work.target.bucket), work.target.deviceID, work.target.pluginID, work.target.pluginVersion, work.target.collectorID, work.target.metricID, incoming.sample.Type, incoming.sample.Unit, string(labels)).Scan(&kind, &summary, &quality, &count, &lastTimestamp)
		if errors.Is(err, sql.ErrNoRows) {
			encoded, aggregateKind, sampleCount, last, summarizeErr := summarizePluginSeries(incoming.sample.Type, []aggregateValue{{at: incoming.at, value: incoming.sample.Value, quality: incoming.sample.Quality}})
			if summarizeErr != nil {
				return updated, summarizeErr
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_metric_aggregates
				(resolution_seconds, bucket_start, device_id, plugin_id, plugin_version, collector_id, metric_id, metric_type, unit, labels_json, aggregation_kind, summary_json, quality, sample_count, first_observed_at, last_observed_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'partial', ?, ?, ?)`, work.target.resolution, storageTimestamp(work.target.bucket), work.target.deviceID, work.target.pluginID, work.target.pluginVersion, work.target.collectorID, work.target.metricID, incoming.sample.Type, incoming.sample.Unit, string(labels), aggregateKind, encoded, sampleCount, storageTimestamp(last), storageTimestamp(last)); err != nil {
				return updated, fmt.Errorf("insert partial plugin metric aggregate: %w", err)
			}
			updated++
			continue
		}
		if err != nil {
			return updated, fmt.Errorf("read plugin metric aggregate: %w", err)
		}
		last, err := time.Parse(storageTimeLayout, lastTimestamp)
		if err != nil {
			return updated, fmt.Errorf("parse plugin metric aggregate cursor: %w", err)
		}
		if incoming.at.Before(last) {
			return updated, errors.New("plugin metric observation is older than the exact aggregation window")
		}
		encoded, err := mergePluginSummary(incoming.sample, summary, count)
		if err != nil {
			return updated, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE plugin_metric_aggregates SET summary_json = ?, sample_count = sample_count + 1, last_observed_at = ?
			WHERE resolution_seconds = ? AND bucket_start = ? AND device_id = ? AND plugin_id = ? AND plugin_version = ? AND collector_id = ? AND metric_id = ? AND metric_type = ? AND unit = ? AND labels_json = ?`, encoded, storageTimestamp(incoming.at), work.target.resolution, storageTimestamp(work.target.bucket), work.target.deviceID, work.target.pluginID, work.target.pluginVersion, work.target.collectorID, work.target.metricID, incoming.sample.Type, incoming.sample.Unit, string(labels)); err != nil {
			return updated, fmt.Errorf("update partial plugin metric aggregate: %w", err)
		}
		_ = kind
		_ = quality
		updated++
	}
	return updated, nil
}

func mergePluginSummary(sample metrics.PluginMetricObservation, encoded []byte, count int) ([]byte, error) {
	var summary map[string]any
	if err := json.Unmarshal(encoded, &summary); err != nil {
		return nil, errors.New("stored plugin metric summary is invalid")
	}
	switch sample.Type {
	case "gauge":
		var value float64
		if err := json.Unmarshal(sample.Value, &value); err != nil {
			return nil, errors.New("numeric plugin metric requires finite JSON numbers")
		}
		mean, okMean := summary["mean"].(float64)
		min, okMin := summary["min"].(float64)
		max, okMax := summary["max"].(float64)
		if !okMean || !okMin || !okMax {
			return nil, errors.New("stored gauge summary is invalid")
		}
		summary["mean"] = (mean*float64(count) + value) / float64(count+1)
		summary["min"] = math.Min(min, value)
		summary["max"] = math.Max(max, value)
		summary["last"] = value
		summary["sample_count"] = count + 1
	case "counter":
		var value float64
		if err := json.Unmarshal(sample.Value, &value); err != nil {
			return nil, errors.New("numeric plugin metric requires finite JSON numbers")
		}
		last, okLast := summary["last"].(float64)
		delta, okDelta := summary["delta"].(float64)
		resets, okResets := summary["reset_count"].(float64)
		if !okLast || !okDelta || !okResets {
			return nil, errors.New("stored counter summary is invalid")
		}
		if sample.Quality == "reset" || value < last {
			delta += value
			resets++
		} else {
			delta += value - last
		}
		summary["delta"], summary["last"], summary["reset_count"], summary["sample_count"] = delta, value, resets, count+1
	case "state":
		var value any
		if err := json.Unmarshal(sample.Value, &value); err != nil {
			return nil, errors.New("state plugin metric requires valid JSON")
		}
		transitions, ok := summary["transition_count"].(float64)
		if !ok {
			return nil, errors.New("stored state summary is invalid")
		}
		previous, _ := json.Marshal(summary["last"])
		current, _ := json.Marshal(value)
		if !bytes.Equal(previous, current) {
			transitions++
		}
		summary["last"], summary["transition_count"], summary["sample_count"] = value, transitions, count+1
	case "event":
		events, ok := summary["event_count"].(float64)
		if !ok {
			return nil, errors.New("stored event summary is invalid")
		}
		summary["event_count"], summary["sample_count"] = events+1, count+1
	default:
		return nil, errors.New("unsupported plugin metric type")
	}
	return json.Marshal(summary)
}

func summarizePluginSeries(typ string, values []aggregateValue) ([]byte, string, int, time.Time, error) {
	valid := make([]aggregateValue, 0, len(values))
	for _, value := range values {
		if value.quality != "missing" && value.quality != "stale" {
			valid = append(valid, value)
		}
	}
	if len(valid) == 0 {
		return nil, "", 0, time.Time{}, nil
	}
	sort.SliceStable(valid, func(i, j int) bool { return valid[i].at.Before(valid[j].at) })
	last := valid[len(valid)-1].at
	switch typ {
	case "gauge":
		numbers, err := numericValues(valid)
		if err != nil {
			return nil, "", 0, time.Time{}, err
		}
		sum, min, max := 0.0, numbers[0], numbers[0]
		for _, number := range numbers {
			sum += number
			min = math.Min(min, number)
			max = math.Max(max, number)
		}
		encoded, _ := json.Marshal(map[string]any{"mean": sum / float64(len(numbers)), "min": min, "max": max, "last": numbers[len(numbers)-1], "sample_count": len(numbers)})
		return encoded, "gauge_summary", len(numbers), last, nil
	case "counter":
		numbers, err := numericValues(valid)
		if err != nil {
			return nil, "", 0, time.Time{}, err
		}
		delta, resets := 0.0, 0
		if valid[0].quality == "reset" {
			resets = 1
		}
		for index := 1; index < len(numbers); index++ {
			if valid[index].quality == "reset" || numbers[index] < numbers[index-1] {
				delta += numbers[index]
				resets++
			} else {
				delta += numbers[index] - numbers[index-1]
			}
		}
		encoded, _ := json.Marshal(map[string]any{"delta": delta, "first": numbers[0], "last": numbers[len(numbers)-1], "reset_count": resets, "sample_count": len(numbers)})
		return encoded, "counter_delta", len(numbers), last, nil
	case "state":
		transitions := 0
		for index := 1; index < len(valid); index++ {
			if !bytes.Equal(valid[index-1].value, valid[index].value) {
				transitions++
			}
		}
		var lastValue any
		_ = json.Unmarshal(valid[len(valid)-1].value, &lastValue)
		encoded, _ := json.Marshal(map[string]any{"last": lastValue, "transition_count": transitions, "sample_count": len(valid)})
		return encoded, "state_summary", len(valid), last, nil
	case "event":
		encoded, _ := json.Marshal(map[string]any{"event_count": len(valid), "sample_count": len(valid)})
		return encoded, "event_count", len(valid), last, nil
	default:
		return nil, "", 0, time.Time{}, errors.New("unsupported plugin metric type")
	}
}

func numericValues(values []aggregateValue) ([]float64, error) {
	result := make([]float64, len(values))
	for index, value := range values {
		if err := json.Unmarshal(value.value, &result[index]); err != nil || math.IsNaN(result[index]) || math.IsInf(result[index], 0) {
			return nil, errors.New("numeric plugin metric requires finite JSON numbers")
		}
	}
	return result, nil
}

func (m *PluginMetricAggregationManager) finish(started time.Time, result PluginAggregationObservation) PluginAggregationObservation {
	result.Duration = time.Since(started)
	m.observe(result)
	return result
}
