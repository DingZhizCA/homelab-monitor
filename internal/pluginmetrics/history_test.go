/*
Package pluginmetrics tests authorization, budgets, and detached typed values.
*/
package pluginmetrics

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

type historySourceFake struct{ points []HistoricalPoint }

func (f historySourceFake) QueryPluginHistory(context.Context, HistoryRequest) ([]HistoricalPoint, error) {
	return f.points, nil
}

func TestHistoryServiceBoundsAndReturnsStructuredIdentity(t *testing.T) {
	from := time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC)
	value := json.RawMessage(`42`)
	service, err := NewHistoryService(historySourceFake{[]HistoricalPoint{{ObservedAt: from.Add(time.Minute), Type: "gauge", Unit: "celsius", Value: value, Quality: "valid", Labels: map[string]string{"disk": "sda"}, AggregationKind: "raw"}}}, HistoryBudget{MaxRange: time.Hour, MaxPoints: 10})
	if err != nil {
		t.Fatal(err)
	}
	request := HistoryRequest{DeviceID: "node-a", PluginID: "smart", PluginVersion: "1.0.0", CollectorID: "temperature", MetricID: "temperature", From: from, To: from.Add(time.Hour), Limit: 5}
	result, err := service.Query(context.Background(), auth.Principal{UserID: "viewer", Role: auth.RoleViewer}, request)
	if err != nil || result.PluginVersion != "1.0.0" || len(result.Points) != 1 || result.Points[0].Labels["disk"] != "sda" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	value[0] = '0'
	if string(result.Points[0].Value) != "42" {
		t.Fatal("result retained source-owned JSON")
	}
	request.Limit = 11
	if _, err := service.Query(context.Background(), auth.Principal{UserID: "viewer", Role: auth.RoleViewer}, request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("budget error=%v", err)
	}
}
