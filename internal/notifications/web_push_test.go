/* Package notifications verifies stable Web Push response classification. */
package notifications

import (
	"net/http"
	"testing"
)

func TestClassifyPushStatus(t *testing.T) {
	for status, want := range map[int]PushFailureClass{http.StatusGone: PushFailurePermanent, http.StatusUnauthorized: PushFailurePermanent, http.StatusTooManyRequests: PushFailureTransient, http.StatusServiceUnavailable: PushFailureTransient} {
		if got := ClassifyPushStatus(status); got != want {
			t.Fatalf("status=%d got=%q want=%q", status, got, want)
		}
	}
}
