/*
Package notifications verifies deterministic per-user materialization and the
bounded fanout handler's source-to-sink handoff.
*/
package notifications

import (
	"context"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
)

func TestBuildNotificationIsDeterministicAndPreservesChannels(t *testing.T) {
	intent := subscriptionIntent(t, alerts.SeverityWarning)
	recipient := Recipient{UserID: "user-a", Channels: Channels{InApp: true, Push: true}}
	first, err := BuildNotification(intent, recipient)
	if err != nil || ValidateNotification(first) != nil {
		t.Fatalf("notification=%+v err=%v", first, err)
	}
	second, err := BuildNotification(intent, recipient)
	if err != nil || second != first {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestFanoutHandlerUsesExplicitBoundAndSink(t *testing.T) {
	intent := subscriptionIntent(t, alerts.SeverityCritical)
	source := &fanoutRecipients{result: []Recipient{{UserID: "user-a", Channels: Channels{InApp: true}}}}
	sink := &fanoutSink{}
	handler, err := NewFanoutHandler(source, sink, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if source.limit != 3 || sink.intent.ID != intent.ID || len(sink.recipients) != 1 {
		t.Fatalf("limit=%d intent=%+v recipients=%+v", source.limit, sink.intent, sink.recipients)
	}
}

type fanoutRecipients struct {
	limit  int
	result []Recipient
}

func (s *fanoutRecipients) Recipients(_ context.Context, _ Intent, limit int) ([]Recipient, error) {
	s.limit = limit
	return append([]Recipient(nil), s.result...), nil
}

type fanoutSink struct {
	intent     Intent
	recipients []Recipient
}

func (s *fanoutSink) Materialize(_ context.Context, intent Intent, recipients []Recipient) error {
	s.intent = intent
	s.recipients = append([]Recipient(nil), recipients...)
	return nil
}
