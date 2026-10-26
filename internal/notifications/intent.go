/*
Package notifications converts immutable Incident transitions into delivery
independent notification intents. It does not select recipients, apply quiet
hours, contact external push services, or choose retry policy.
*/
package notifications

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
)

var ErrIntentCapacity = errors.New("notification intent outbox capacity reached")

type IntentKind string

const (
	IntentFiring   IntentKind = "firing"
	IntentRecovery IntentKind = "recovery"
)

type DeliveryPolicy string

const (
	DeliveryPushEligible DeliveryPolicy = "push_eligible"
	DeliveryRecordOnly   DeliveryPolicy = "record_only"
)

// Intent is a deduplicated fact awaiting recipient and channel planning.
type Intent struct {
	ID             string          `json:"id"`
	TransitionID   string          `json:"transition_id"`
	DeviceID       string          `json:"device_id"`
	RuleID         string          `json:"rule_id"`
	RuleRevision   uint64          `json:"rule_revision"`
	Severity       alerts.Severity `json:"severity"`
	Kind           IntentKind      `json:"kind"`
	DeliveryPolicy DeliveryPolicy  `json:"delivery_policy"`
	CreatedAt      time.Time       `json:"created_at"`
}

// Plan emits Firing and recovery facts while suppressing intermediate changes.
func Plan(transition alerts.IncidentTransition, severity alerts.Severity) (Intent, bool, error) {
	if err := alerts.ValidateIncidentTransition(transition); err != nil {
		return Intent{}, false, err
	}
	if severity != alerts.SeverityCritical && severity != alerts.SeverityWarning && severity != alerts.SeverityInfo {
		return Intent{}, false, errors.New("notification severity is invalid")
	}
	var kind IntentKind
	switch {
	case transition.ToStatus == alerts.IncidentFiring && transition.FromStatus != alerts.IncidentRecovering:
		kind = IntentFiring
	case transition.ToStatus == alerts.IncidentResolved && (transition.FromStatus == alerts.IncidentFiring || transition.FromStatus == alerts.IncidentRecovering):
		kind = IntentRecovery
	default:
		return Intent{}, false, nil
	}
	policy := DeliveryPushEligible
	if severity == alerts.SeverityInfo {
		policy = DeliveryRecordOnly
	}
	intent := Intent{
		TransitionID: transition.ID, DeviceID: transition.DeviceID, RuleID: transition.RuleID,
		RuleRevision: transition.RuleRevision, Severity: severity, Kind: kind,
		DeliveryPolicy: policy, CreatedAt: transition.OccurredAt,
	}
	intent.ID = intentID(intent.TransitionID, intent.Kind)
	return intent, true, nil
}

// ValidateIntent verifies a persisted outbox fact without external lookups.
func ValidateIntent(intent Intent) error {
	if len(intent.ID) != sha256.Size*2 {
		return errors.New("notification intent ID is invalid")
	}
	if _, err := hex.DecodeString(intent.ID); err != nil {
		return errors.New("notification intent ID is invalid")
	}
	if len(intent.TransitionID) != sha256.Size*2 || strings.TrimSpace(intent.DeviceID) == "" || strings.TrimSpace(intent.DeviceID) != intent.DeviceID || strings.TrimSpace(intent.RuleID) == "" || intent.RuleRevision == 0 || intent.CreatedAt.IsZero() {
		return errors.New("notification intent identity is invalid")
	}
	if _, err := hex.DecodeString(intent.TransitionID); err != nil {
		return errors.New("notification intent transition ID is invalid")
	}
	if intent.Kind != IntentFiring && intent.Kind != IntentRecovery {
		return errors.New("notification intent kind is invalid")
	}
	expectedPolicy := DeliveryPushEligible
	if intent.Severity == alerts.SeverityInfo {
		expectedPolicy = DeliveryRecordOnly
	} else if intent.Severity != alerts.SeverityCritical && intent.Severity != alerts.SeverityWarning {
		return errors.New("notification intent severity is invalid")
	}
	if intent.DeliveryPolicy != expectedPolicy || intent.ID != intentID(intent.TransitionID, intent.Kind) {
		return errors.New("notification intent policy or deduplication key is invalid")
	}
	return nil
}

func intentID(transitionID string, kind IntentKind) string {
	sum := sha256.Sum256([]byte(transitionID + "\x00" + string(kind)))
	return hex.EncodeToString(sum[:])
}
