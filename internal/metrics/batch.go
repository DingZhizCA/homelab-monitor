/*
Package metrics defines an idempotent envelope for bounded historical uploads.

The batch identity is supplied by the caller so a retry can reuse the same key;
snapshots are validated as one device's strictly ordered sequence.
*/
package metrics

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	ErrBatchConflict         = errors.New("batch ID was previously used with different contents")
	ErrMetricStorageCapacity = errors.New("metric storage capacity is exhausted")
)

type Batch struct {
	ID        string           `json:"batch_id"`
	DeviceID  string           `json:"device_id"`
	Snapshots []MetricEnvelope `json:"snapshots"`
}

var batchIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// NewBatchID returns a 128-bit random id suitable for identifying one retryable batch.
// Callers must retain the returned value with the batch and reuse it for every retry.
func NewBatchID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate batch ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func (b Batch) Validate(maxSnapshots int) error {
	if !batchIDPattern.MatchString(b.ID) || strings.TrimSpace(b.DeviceID) == "" || strings.TrimSpace(b.DeviceID) != b.DeviceID {
		return errors.New("batch ID and device ID are required")
	}
	if maxSnapshots < 1 || len(b.Snapshots) == 0 || len(b.Snapshots) > maxSnapshots {
		return fmt.Errorf("batch must contain between 1 and %d snapshots", maxSnapshots)
	}
	var previousTimestamp time.Time
	for index, snapshot := range b.Snapshots {
		if snapshot.Device.ID != b.DeviceID {
			return fmt.Errorf("snapshot %d belongs to a different device", index)
		}
		if err := snapshot.Validate(); err != nil {
			return fmt.Errorf("snapshot %d is invalid: %w", index, err)
		}
		if index > 0 && !snapshot.Timestamp.After(previousTimestamp) {
			return fmt.Errorf("snapshot %d is not later than the previous snapshot", index)
		}
		previousTimestamp = snapshot.Timestamp
	}
	return nil
}
