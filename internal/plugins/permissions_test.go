package plugins

import "testing"

func TestGrantAuthorizerRequiresExactPluginPublisherAndCapabilities(t *testing.T) {
	authorizer, err := NewGrantAuthorizer([]PermissionGrant{{
		PluginID: "network", Publisher: "trusted.example", Permissions: []string{"read:proc"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	manifest := validManifest()
	manifest.PluginID = "network"
	manifest.Publisher = "trusted.example"
	manifest.Permissions = []string{"read:proc"}
	if err := authorizer.AuthorizePlugin(manifest); err != nil {
		t.Fatalf("approved capability rejected: %v", err)
	}
	manifest.Permissions = append(manifest.Permissions, "remote:openwrt_metrics")
	if err := authorizer.AuthorizePlugin(manifest); err == nil {
		t.Fatal("unapproved capability was accepted")
	}
	manifest.Permissions = []string{"read:proc"}
	manifest.Publisher = "other.example"
	if err := authorizer.AuthorizePlugin(manifest); err == nil {
		t.Fatal("unapproved publisher was accepted")
	}
	manifest.Publisher = "trusted.example"
	manifest.PluginID = "other"
	if err := authorizer.AuthorizePlugin(manifest); err == nil {
		t.Fatal("unapproved plugin ID was accepted")
	}
}

func TestGrantAuthorizerValidatesAndCopiesGrants(t *testing.T) {
	grant := PermissionGrant{PluginID: "network", Publisher: "trusted.example", Permissions: []string{"read:proc"}}
	authorizer, err := NewGrantAuthorizer([]PermissionGrant{grant})
	if err != nil {
		t.Fatal(err)
	}
	grant.Permissions[0] = "remote:openwrt_metrics"
	manifest := validManifest()
	manifest.PluginID = "network"
	manifest.Publisher = "trusted.example"
	manifest.Permissions = []string{"read:proc"}
	if err := authorizer.AuthorizePlugin(manifest); err != nil {
		t.Fatalf("mutating caller grant changed copied authorizer policy: %v", err)
	}
	if _, err := NewGrantAuthorizer([]PermissionGrant{grant, grant}); err == nil {
		t.Fatal("duplicate plugin publisher grants were accepted")
	}
	if _, err := NewGrantAuthorizer([]PermissionGrant{{PluginID: "../network", Publisher: "trusted.example", Permissions: []string{"read:proc"}}}); err == nil {
		t.Fatal("grant with invalid plugin ID was accepted")
	}
}

func TestGrantAuthorizerRequiresPackageGrantWithoutCapabilities(t *testing.T) {
	authorizer, err := NewGrantAuthorizer([]PermissionGrant{{PluginID: "network", Publisher: "trusted.example"}})
	if err != nil {
		t.Fatal(err)
	}
	manifest := validManifest()
	manifest.PluginID = "network"
	manifest.Publisher = "trusted.example"
	manifest.Permissions = nil
	if err := authorizer.AuthorizePlugin(manifest); err != nil {
		t.Fatalf("explicit package grant rejected: %v", err)
	}
	manifest.Publisher = "untrusted.example"
	if err := authorizer.AuthorizePlugin(manifest); err == nil {
		t.Fatal("package without an explicit grant was accepted")
	}
}
