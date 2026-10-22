/*
Package agent connects plugin control polling to local reconciliation.

This file runs exactly one caller-requested poll, reconcile, and report cycle.
It does not own a timer, start plugins itself, or affect metric collection cadence.
*/
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

// PluginReconcileControl exchanges desired and reported plugin state with Server.
type PluginReconcileControl interface {
	Desired(context.Context, string) ([]plugins.DesiredState, error)
	Reported(context.Context, string, []plugins.ReportedState) error
}

// PluginStateReconciler applies one explicit desired-state batch on this Agent.
type PluginStateReconciler interface {
	Apply(context.Context, []plugins.DesiredState) ([]plugins.ReportedState, error)
}

// ReconcilePluginsOnce pulls, applies, and reports one plugin state batch.
func ReconcilePluginsOnce(ctx context.Context, deviceID string, control PluginReconcileControl, reconciler PluginStateReconciler) error {
	if ctx == nil || strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID || control == nil || reconciler == nil {
		return errors.New("context, device ID, plugin control client, and reconciler are required")
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("plugin reconciliation context must have a deadline")
	}
	desired, err := control.Desired(ctx, deviceID)
	if err != nil {
		return fmt.Errorf("poll plugin desired state: %w", err)
	}
	reported, reconcileErr := reconciler.Apply(ctx, desired)
	if len(reported) == 0 {
		return reconcileErr
	}
	if err := control.Reported(ctx, deviceID, reported); err != nil {
		return errors.Join(reconcileErr, fmt.Errorf("report plugin state: %w", err))
	}
	return reconcileErr
}
