package provider

import "testing"

func TestGitProviderDeclaresInactiveHookSchema(t *testing.T) {
	item := NewGit()
	value, active, err := item.Load(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if active || value != nil {
		t.Fatalf("value = %#v, active = %t", value, active)
	}
	schema := item.Schema()
	if _, ok := schema.Fields["repository"]; !ok {
		t.Fatal("repository schema is missing")
	}
	hook, ok := schema.Fields["hook"]
	if !ok || !hook.Fields["payload"].Open {
		t.Fatalf("hook schema = %#v", hook)
	}
}
