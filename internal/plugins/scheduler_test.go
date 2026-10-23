/*
Package plugins tests bounded collection dispatch and per-task failure isolation.
*/
package plugins

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type gatedSchedulerPlugin struct {
	testPlugin
	entered chan struct{}
	release chan struct{}
}

func (p gatedSchedulerPlugin) Collect(ctx context.Context, _ CollectRequest) (CollectResult, error) {
	p.entered <- struct{}{}
	select {
	case <-p.release:
		return CollectResult{ObservedAt: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), Samples: []MetricSample{{MetricID: "network.rx_bytes", Value: json.RawMessage(`1`), Quality: QualityValid, Labels: map[string]string{"interface": "eth0"}}}}, nil
	case <-ctx.Done():
		return CollectResult{}, ctx.Err()
	}
}

func TestPluginSchedulerKeepsBatchOrderAndIsolatesFailures(t *testing.T) {
	stager, staged := stageManagerFixture(t, "network.flow", ProtocolRange{Min: 1, Max: 1})
	process := &managerProcess{socketPath: managerSocket(t)}
	manager, err := NewPluginManager(stager, &managerSupervisor{process: process}, 1, managerPermissionPolicy(allowManagerTestPlugin))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	plugin, err := manager.Prepare(ctx, staged.RelativePath)
	if err != nil {
		t.Fatal(err)
	}
	defer plugin.Close(ctx)

	scheduler, err := NewPluginScheduler(2)
	if err != nil {
		t.Fatal(err)
	}
	tasks := []CollectionTask{
		{Plugin: plugin, CollectorID: "network.flow", Config: json.RawMessage(`{}`), Timeout: time.Second},
		{Plugin: plugin, CollectorID: "missing.collector", Config: json.RawMessage(`{}`), Timeout: time.Second},
	}
	outcomes, err := scheduler.CollectBatch(ctx, tasks)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 2 || outcomes[0].Err != nil || len(outcomes[0].Result.Samples) != 1 || outcomes[0].PluginVersion != staged.Manifest.Version || outcomes[0].Descriptor.ID != "network.flow" {
		t.Fatalf("first collection outcome = %+v", outcomes)
	}
	if outcomes[1].Err == nil || outcomes[1].CollectorID != "missing.collector" {
		t.Fatalf("failed task outcome = %+v", outcomes[1])
	}
	if tasks[0].Config[0] != '{' {
		t.Fatal("scheduler modified caller-owned task data")
	}
}

func TestPluginSchedulerRequiresExplicitConcurrencyAndDeadline(t *testing.T) {
	if _, err := NewPluginScheduler(0); err == nil {
		t.Fatal("zero concurrency was accepted")
	}
	scheduler, err := NewPluginScheduler(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.CollectBatch(context.Background(), nil); err == nil {
		t.Fatal("batch without a deadline was accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := scheduler.CollectBatch(ctx, []CollectionTask{{CollectorID: "x", Config: json.RawMessage("invalid")}}); err == nil {
		t.Fatal("invalid task was accepted")
	}
}

func TestPluginSchedulerCapsConcurrencyAcrossConcurrentBatches(t *testing.T) {
	implementation := gatedSchedulerPlugin{testPlugin: testPlugin{minHostMajor: 1, maxHostMajor: 1}, entered: make(chan struct{}, 2), release: make(chan struct{})}
	host, err := NewGRPCHost(startTestPlugin(t, implementation))
	if err != nil {
		t.Fatal(err)
	}
	defer host.conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := host.Handshake(ctx, HostIdentity{PluginID: "network", Version: "1.0.0", ProtocolMajor: 1}); err != nil {
		t.Fatal(err)
	}
	collectors, err := host.Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prepared := &PreparedPlugin{Package: StagedPackage{Manifest: Manifest{PluginID: "network", Version: "1.0.0"}}, Collectors: collectors, host: host}
	scheduler, err := NewPluginScheduler(1)
	if err != nil {
		t.Fatal(err)
	}
	task := []CollectionTask{{Plugin: prepared, CollectorID: "network.flow", Config: json.RawMessage(`{}`), Timeout: time.Second}}
	results := make(chan error, 2)
	go func() { _, err := scheduler.CollectBatch(ctx, task); results <- err }()
	select {
	case <-implementation.entered:
	case <-time.After(time.Second):
		t.Fatal("first batch did not enter plugin collection")
	}
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		_, err := scheduler.CollectBatch(ctx, task)
		results <- err
	}()
	<-secondStarted
	select {
	case <-implementation.entered:
		t.Fatal("concurrent batch exceeded the scheduler-wide concurrency limit")
	case <-time.After(30 * time.Millisecond):
	}
	close(implementation.release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("scheduled batch failed: %v", err)
		}
	}
}
