/*
Package notifications provides the encrypted VAPID Web Push transport boundary.
Subscription persistence and scheduling remain separate from network delivery.
*/
package notifications

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	webpush "github.com/SherClockHolmes/webpush-go"
)

type PushFailureClass string

const (
	PushFailureTransient PushFailureClass = "transient"
	PushFailurePermanent PushFailureClass = "permanent"
)

type PushSubscription struct {
	Endpoint string `json:"endpoint"`
	P256DH   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

func (s PushSubscription) Validate(maxEndpointBytes, maxKeyBytes int) error {
	parsed, err := url.Parse(s.Endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || len(s.Endpoint) > maxEndpointBytes || len(s.P256DH) == 0 || len(s.P256DH) > maxKeyBytes || len(s.Auth) == 0 || len(s.Auth) > maxKeyBytes {
		return errors.New("Web Push subscription is invalid")
	}
	return nil
}

type WebPushConfig struct {
	Subscriber, PublicKey, PrivateKey string
	TTL                               int
	MaxPayloadBytes                   int
}

type WebPushAdapter struct {
	config WebPushConfig
	client webpush.HTTPClient
}

func NewWebPushAdapter(config WebPushConfig, client webpush.HTTPClient) (*WebPushAdapter, error) {
	if strings.TrimSpace(config.Subscriber) == "" || config.PublicKey == "" || config.PrivateKey == "" || config.TTL < 1 || config.MaxPayloadBytes < 1 || client == nil {
		return nil, errors.New("Web Push requires VAPID identity, keys, TTL, payload limit, and HTTP client")
	}
	return &WebPushAdapter{config: config, client: client}, nil
}

func (a *WebPushAdapter) Send(ctx context.Context, subscription PushSubscription, payload []byte) (PushFailureClass, error) {
	if err := subscription.Validate(4096, 1024); err != nil || len(payload) == 0 || len(payload) > a.config.MaxPayloadBytes {
		return PushFailurePermanent, errors.New("Web Push request is invalid")
	}
	response, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{Endpoint: subscription.Endpoint, Keys: webpush.Keys{P256dh: subscription.P256DH, Auth: subscription.Auth}}, &webpush.Options{Subscriber: a.config.Subscriber, VAPIDPublicKey: a.config.PublicKey, VAPIDPrivateKey: a.config.PrivateKey, TTL: a.config.TTL, HTTPClient: a.client})
	if err != nil {
		return PushFailureTransient, fmt.Errorf("send Web Push: %w", err)
	}
	defer response.Body.Close()
	class := ClassifyPushStatus(response.StatusCode)
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return class, nil
	}
	return class, fmt.Errorf("Web Push service returned status %d", response.StatusCode)
}

func ClassifyPushStatus(status int) PushFailureClass {
	if status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500 {
		return PushFailureTransient
	}
	return PushFailurePermanent
}
