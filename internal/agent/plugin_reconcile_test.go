package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

type pluginReconcileControlStub struct {
	desired       []plugins.DesiredState
	desiredErr    error
	reported      []plugins.ReportedState
	reportedErr   error
	desiredCalls  int
	reportedCalls int
}

func (s *pluginReconcileControlStub) Desired(_ context.Context, deviceID string) ([]plugins.DesiredState, error) {
	s.desiredCalls++
	if len(s.desired) > 0 && s.desired[0].DeviceID != deviceID {
		return nil, errors.New("unexpected device ID")
	}
	return s.desired, s.desiredErr
}

func (s *pluginReconcileControlStub) Reported(_ context.Context, deviceID string, states []plugins.ReportedState) error {
	s.reportedCalls++
	for _, state := range states {
		if state.DeviceID != deviceID {
			return errors.New("unexpected report device ID")
		}
	}
	s.reported = append([]plugins.ReportedState(nil), states...)
	return s.reportedErr
}

type pluginStateReconcilerStub struct {
	reports []plugins.ReportedState
	err     error
	calls   int
}

func (s *pluginStateReconcilerStub) Apply(_ context.Context, desired []plugins.DesiredState) ([]plugins.ReportedState, error) {
	s.calls++
	if len(desired) != 0 && len(s.reports) != 0 && desired[0].DeviceID != s.reports[0].DeviceID {
		return nil, errors.New("unexpected desired state")
	}
	return s.reports, s.err
}

func TestReconcilePluginsOncePollsAppliesAndReports(t *testing.T) {
	state := plugins.DesiredState{DeviceID: "node-a", PluginID: "health", Revision: 1}
	report := plugins.ReportedState{DeviceID: "node-a", PluginID: "health", ObservedRev: 1, Lifecycle: plugins.StateFailed}
	control := &pluginReconcileControlStub{desired: []plugins.DesiredState{state}}
	reconciler := &pluginStateReconcilerStub{reports: []plugins.ReportedState{report}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ReconcilePluginsOnce(ctx, "node-a", control, reconciler); err != nil {
		t.Fatal(err)
	}
	if control.desiredCalls != 1 || reconciler.calls != 1 || control.reportedCalls != 1 || len(control.reported) != 1 || control.reported[0] != report {
		t.Fatalf("poll/apply/report calls = %d/%d/%d, reports=%+v", control.desiredCalls, reconciler.calls, control.reportedCalls, control.reported)
	}
}

func TestReconcilePluginsOnceReportsPartialFailures(t *testing.T) {
	reconcileErr := errors.New("one plugin failed")
	reportErr := errors.New("server report failed")
	report := plugins.ReportedState{DeviceID: "node-a", PluginID: "health", ObservedRev: 3, Lifecycle: plugins.StateFailed}
	control := &pluginReconcileControlStub{desired: []plugins.DesiredState{{DeviceID: "node-a"}}, reportedErr: reportErr}
	reconciler := &pluginStateReconcilerStub{reports: []plugins.ReportedState{report}, err: reconcileErr}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := ReconcilePluginsOnce(ctx, "node-a", control, reconciler)
	if !errors.Is(err, reconcileErr) || !errors.Is(err, reportErr) {
		t.Fatalf("combined reconcile/report error = %v", err)
	}
	if control.reportedCalls != 1 {
		t.Fatalf("reported calls = %d, want 1", control.reportedCalls)
	}
}

func TestReconcilePluginsOnceStopsAfterDesiredPollFailure(t *testing.T) {
	pollErr := errors.New("server unavailable")
	control := &pluginReconcileControlStub{desiredErr: pollErr}
	reconciler := &pluginStateReconcilerStub{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := ReconcilePluginsOnce(ctx, "node-a", control, reconciler)
	if !errors.Is(err, pollErr) || reconciler.calls != 0 || control.reportedCalls != 0 {
		t.Fatalf("poll failure flow err=%v apply=%d report=%d", err, reconciler.calls, control.reportedCalls)
	}
}

func TestReconcilePluginsOnceRequiresBoundedContext(t *testing.T) {
	control := &pluginReconcileControlStub{}
	reconciler := &pluginStateReconcilerStub{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ReconcilePluginsOnce(ctx, " node-a ", control, reconciler); err == nil {
		t.Fatal("untrimmed plugin reconciliation device ID was accepted")
	}
	if err := ReconcilePluginsOnce(context.Background(), "node-a", control, reconciler); err == nil {
		t.Fatal("unbounded plugin reconciliation context was accepted")
	}
	if control.desiredCalls != 0 || reconciler.calls != 0 || control.reportedCalls != 0 {
		t.Fatalf("unbounded context reached components: poll=%d apply=%d report=%d", control.desiredCalls, reconciler.calls, control.reportedCalls)
	}
}
