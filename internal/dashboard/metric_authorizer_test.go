/*
Package dashboard tests the approved Owner and Viewer metric read policy.
*/
package dashboard

import (
	"errors"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

func TestAuthenticatedMetricAuthorizerAllowsHumanRoles(t *testing.T) {
	policy := AuthenticatedMetricAuthorizer{}
	for _, role := range []auth.Role{auth.RoleOwner, auth.RoleViewer} {
		if err := policy.AuthorizeMetric(auth.Principal{UserID: "user-a", Role: role}, "cpu.usage_percent", "node-a"); err != nil {
			t.Fatalf("role %s rejected: %v", role, err)
		}
	}
	for _, principal := range []auth.Principal{{}, {UserID: "user-a", Role: "unknown"}} {
		if err := policy.AuthorizeMetric(principal, "cpu.usage_percent", "node-a"); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("principal %+v error=%v", principal, err)
		}
	}
}
