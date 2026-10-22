/*
Package plugins provides bounded, caller-driven plugin collection dispatch.

The scheduler does not create a periodic timer or change device sampling
cadence. The Agent chooses when a batch is due; each result is isolated to its
own task and collected under the caller's deadline.
*/
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// CollectionTask describes one plugin collector invocation in a caller-owned batch.
type CollectionTask struct {
	Plugin      *PreparedPlugin
	CollectorID string
	Config      json.RawMessage
}

// CollectionOutcome preserves each task's result or error without failing its peers.
type CollectionOutcome struct {
	PluginID      string
	PluginVersion string
	CollectorID   string
	Descriptor    CollectorDescriptor
	Result        CollectResult
	Err           error
}

// PluginScheduler bounds concurrency for one explicitly triggered collection batch.
type PluginScheduler struct {
	maxConcurrent int
	slots         chan struct{}
}

// NewPluginScheduler requires a caller-selected concurrency limit.
func NewPluginScheduler(maxConcurrent int) (*PluginScheduler, error) {
	if maxConcurrent < 1 {
		return nil, errors.New("plugin collection concurrency must be positive")
	}
	return &PluginScheduler{maxConcurrent: maxConcurrent, slots: make(chan struct{}, maxConcurrent)}, nil
}

// CollectBatch dispatches one due batch and returns outcomes in task order.
func (s *PluginScheduler) CollectBatch(ctx context.Context, tasks []CollectionTask) ([]CollectionOutcome, error) {
	if err := requirePluginDeadline(ctx); err != nil {
		return nil, err
	}
	outcomes := make([]CollectionOutcome, len(tasks))
	batch := make([]CollectionTask, len(tasks))
	for i, task := range tasks {
		if task.Plugin == nil || task.CollectorID == "" || !json.Valid(task.Config) {
			return nil, fmt.Errorf("collection task %d requires a plugin, collector ID, and valid JSON config", i)
		}
		outcomes[i] = CollectionOutcome{
			PluginID:      task.Plugin.Package.Manifest.PluginID,
			PluginVersion: task.Plugin.Package.Manifest.Version,
			CollectorID:   task.CollectorID,
			Descriptor:    findCollectorDescriptor(task.Plugin.Collectors, task.CollectorID),
		}
		batch[i] = task
		batch[i].Config = append(json.RawMessage(nil), task.Config...)
	}
	if len(tasks) == 0 {
		return outcomes, nil
	}
	workers := min(s.maxConcurrent, len(tasks))
	indices := make(chan int, len(tasks))
	for index := range tasks {
		indices <- index
	}
	close(indices)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			for index := range indices {
				select {
				case s.slots <- struct{}{}:
				case <-ctx.Done():
					outcomes[index].Err = ctx.Err()
					continue
				}
				task := batch[index]
				result, err := task.Plugin.Collect(ctx, CollectRequest{CollectorID: task.CollectorID, Config: task.Config})
				outcomes[index].Result = result
				outcomes[index].Err = err
				<-s.slots
			}
		}()
	}
	group.Wait()
	return outcomes, nil
}

func findCollectorDescriptor(descriptors []CollectorDescriptor, collectorID string) CollectorDescriptor {
	for _, descriptor := range descriptors {
		if descriptor.ID != collectorID {
			continue
		}
		cloned := CollectorDescriptor{ID: descriptor.ID, Metrics: make([]MetricDescriptor, len(descriptor.Metrics))}
		for i, metric := range descriptor.Metrics {
			cloned.Metrics[i] = metric
			cloned.Metrics[i].LabelKeys = append([]string(nil), metric.LabelKeys...)
		}
		return cloned
	}
	return CollectorDescriptor{}
}
