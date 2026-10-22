/*
Package agent configures HTTP behavior shared by credential-bearing clients.

Agent credentials are scoped to the configured Server endpoint and must not
follow redirects to a different location.
*/
package agent

import (
	"net/http"
	"time"
)

func newCredentialHTTPClient() *http.Client {
	return newCredentialHTTPClientWithTimeout(4 * time.Second)
}

func newCredentialHTTPClientWithTimeout(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
