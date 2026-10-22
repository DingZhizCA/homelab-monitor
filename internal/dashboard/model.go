/*
Package dashboard defines the versioned, storage-independent Dashboard model.

Widget documents contain only stable descriptor references and layout data;
they cannot carry executable expressions or change Agent sampling schedules.
*/
package dashboard

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type Scope string

const (
	ScopePersonal Scope = "personal"
	ScopeShared   Scope = "shared"
	ScopeTemplate Scope = "template"
)

type WidgetKind string

const (
	WidgetMetricCard         WidgetKind = "metric_card"
	WidgetTimeSeries         WidgetKind = "time_series"
	WidgetStatus             WidgetKind = "status"
	WidgetDeviceList         WidgetKind = "device_list"
	WidgetTrafficRanking     WidgetKind = "traffic_ranking"
	WidgetCategorizedTraffic WidgetKind = "categorized_traffic"
	WidgetWiFiClients        WidgetKind = "wifi_clients"
	WidgetTailnetClients     WidgetKind = "tailnet_clients"
	WidgetAlertList          WidgetKind = "alert_list"
	WidgetTopology           WidgetKind = "topology"
)

type Dashboard struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Scope       Scope     `json:"scope"`
	OwnerUserID string    `json:"owner_user_id,omitempty"`
	Draft       *Revision `json:"draft,omitempty"`
	Published   *Revision `json:"published,omitempty"`
}

type Revision struct {
	Number   uint64   `json:"number"`
	Document Document `json:"document"`
}

type Document struct {
	Widgets []Widget `json:"widgets"`
	Layouts []Layout `json:"layouts"`
}

type Widget struct {
	ID           string     `json:"id"`
	Kind         WidgetKind `json:"kind"`
	Title        string     `json:"title"`
	DescriptorID string     `json:"descriptor_id,omitempty"`
	DeviceID     string     `json:"device_id,omitempty"`
}

type Layout struct {
	Breakpoint string      `json:"breakpoint"`
	Items      []Placement `json:"items"`
}

type Placement struct {
	WidgetID string `json:"widget_id"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

var documentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func (d Document) Validate() error {
	widgets := make(map[string]struct{}, len(d.Widgets))
	for index, widget := range d.Widgets {
		if !documentIDPattern.MatchString(widget.ID) {
			return fmt.Errorf("widget %d has an invalid ID", index)
		}
		if _, exists := widgets[widget.ID]; exists {
			return fmt.Errorf("widget ID %q is duplicated", widget.ID)
		}
		widgets[widget.ID] = struct{}{}
		if !validWidgetKind(widget.Kind) {
			return fmt.Errorf("widget %q has an unsupported kind", widget.ID)
		}
		if strings.TrimSpace(widget.Title) == "" || strings.TrimSpace(widget.Title) != widget.Title {
			return fmt.Errorf("widget %q requires a trimmed title", widget.ID)
		}
		if widget.DescriptorID != "" && !documentIDPattern.MatchString(widget.DescriptorID) {
			return fmt.Errorf("widget %q has an invalid descriptor ID", widget.ID)
		}
		if widget.DeviceID != "" && !documentIDPattern.MatchString(widget.DeviceID) {
			return fmt.Errorf("widget %q has an invalid device ID", widget.ID)
		}
	}
	layouts := make(map[string]struct{}, len(d.Layouts))
	for _, layout := range d.Layouts {
		if layout.Breakpoint != "desktop" && layout.Breakpoint != "mobile" {
			return fmt.Errorf("unsupported layout breakpoint %q", layout.Breakpoint)
		}
		if _, exists := layouts[layout.Breakpoint]; exists {
			return fmt.Errorf("layout breakpoint %q is duplicated", layout.Breakpoint)
		}
		layouts[layout.Breakpoint] = struct{}{}
		placed := make(map[string]struct{}, len(layout.Items))
		for _, item := range layout.Items {
			if _, exists := widgets[item.WidgetID]; !exists {
				return fmt.Errorf("layout references unknown widget %q", item.WidgetID)
			}
			if _, exists := placed[item.WidgetID]; exists {
				return fmt.Errorf("widget %q is duplicated in layout %q", item.WidgetID, layout.Breakpoint)
			}
			placed[item.WidgetID] = struct{}{}
			if item.X < 0 || item.Y < 0 || item.Width < 1 || item.Height < 1 {
				return fmt.Errorf("widget %q has invalid layout dimensions", item.WidgetID)
			}
		}
	}
	return nil
}

func validWidgetKind(kind WidgetKind) bool {
	switch kind {
	case WidgetMetricCard, WidgetTimeSeries, WidgetStatus, WidgetDeviceList,
		WidgetTrafficRanking, WidgetCategorizedTraffic, WidgetWiFiClients,
		WidgetTailnetClients, WidgetAlertList, WidgetTopology:
		return true
	default:
		return false
	}
}

var (
	ErrForbidden         = errors.New("dashboard operation forbidden")
	ErrVersionConflict   = errors.New("dashboard revision conflict")
	ErrDashboardNotFound = errors.New("dashboard not found")
	ErrInvalidRequest    = errors.New("invalid dashboard request")
)
