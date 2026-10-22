/*
Package plugins implements explicit local grants for signed plugin capabilities.

GrantAuthorizer matches plugin ID and publisher to an allow-list; the secure
file loader builds that policy from owner-managed configuration. Neither layer
enforces operating-system capabilities.
*/
package plugins

import (
	"errors"
	"fmt"
	"strings"
)

// PermissionGrant is an explicit capability allow-list for one plugin publisher.
type PermissionGrant struct {
	PluginID    string   `json:"plugin_id"`
	Publisher   string   `json:"publisher"`
	Permissions []string `json:"permissions"`
}

type publisherKey struct{ pluginID, publisher string }

// GrantAuthorizer denies requested capabilities unless an exact local grant exists.
type GrantAuthorizer struct {
	grants map[publisherKey]map[string]struct{}
}

// NewGrantAuthorizer validates and copies grants without choosing any defaults.
func NewGrantAuthorizer(grants []PermissionGrant) (*GrantAuthorizer, error) {
	authorizer := &GrantAuthorizer{grants: make(map[publisherKey]map[string]struct{}, len(grants))}
	for _, grant := range grants {
		if !identifierPattern.MatchString(grant.PluginID) || strings.TrimSpace(grant.Publisher) == "" || strings.TrimSpace(grant.Publisher) != grant.Publisher {
			return nil, errors.New("plugin grants require a valid plugin ID and publisher")
		}
		key := publisherKey{pluginID: grant.PluginID, publisher: grant.Publisher}
		if _, exists := authorizer.grants[key]; exists {
			return nil, fmt.Errorf("duplicate permission grant for plugin %q and publisher %q", grant.PluginID, grant.Publisher)
		}
		capabilities := make(map[string]struct{}, len(grant.Permissions))
		for _, permission := range grant.Permissions {
			if !permissionPattern.MatchString(permission) {
				return nil, fmt.Errorf("invalid permission grant %q", permission)
			}
			if _, exists := capabilities[permission]; exists {
				return nil, fmt.Errorf("duplicate permission grant %q", permission)
			}
			capabilities[permission] = struct{}{}
		}
		authorizer.grants[key] = capabilities
	}
	return authorizer, nil
}

// AuthorizePlugin permits only capabilities listed for this plugin and publisher.
func (a *GrantAuthorizer) AuthorizePlugin(manifest Manifest) error {
	if a == nil {
		return errors.New("plugin package grant is unavailable")
	}
	granted, ok := a.grants[publisherKey{pluginID: manifest.PluginID, publisher: manifest.Publisher}]
	if !ok {
		return fmt.Errorf("plugin %q from publisher %q has no local permission grant", manifest.PluginID, manifest.Publisher)
	}
	for _, permission := range manifest.Permissions {
		if _, ok := granted[permission]; !ok {
			return fmt.Errorf("plugin %q is not granted permission %q", manifest.PluginID, permission)
		}
	}
	return nil
}
