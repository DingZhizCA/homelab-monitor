/*
Package dashboard provides the approved first-version metric read policy.

Both active Owner and Viewer principals may read the existing built-in metric
descriptors. Repository and HTTP layers still validate the device, descriptor,
range, and point budget for every request.
*/
package dashboard

import (
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

// AuthenticatedMetricAuthorizer permits built-in metric reads for human roles.
type AuthenticatedMetricAuthorizer struct{}

// AuthorizeMetric accepts active Owner and Viewer principals resolved by auth.Service.
func (AuthenticatedMetricAuthorizer) AuthorizeMetric(principal auth.Principal, _, _ string) error {
	if principal.UserID == "" || (principal.Role != auth.RoleOwner && principal.Role != auth.RoleViewer) {
		return auth.ErrForbidden
	}
	return nil
}

var _ MetricAuthorizer = AuthenticatedMetricAuthorizer{}
