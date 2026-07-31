package persistence

import (
	"context"
	"testing"
)

func TestCreateAndGetRoleMapping(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := store.CreateRoleMapping(ctx, RoleMapping{
		IdentityKind:  "spiffe",
		IdentityValue: "spiffe://grex.example/ns/default/sa/alice",
		Match:         "exact",
		Role:          "admin",
	})
	if err != nil {
		t.Fatalf("CreateRoleMapping: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("CreateRoleMapping did not assign an ID")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("CreateRoleMapping did not set CreatedAt/UpdatedAt")
	}

	got, ok, err := store.GetRoleMapping(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetRoleMapping: %v", err)
	}
	if !ok {
		t.Fatal("GetRoleMapping: want ok = true")
	}
	if got.IdentityKind != "spiffe" || got.IdentityValue != created.IdentityValue ||
		got.Match != "exact" || got.Role != "admin" {
		t.Errorf("GetRoleMapping = %+v, want it to match what was created", got)
	}
}

func TestGetRoleMappingNotFound(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, ok, err := store.GetRoleMapping(ctx, 999999)
	if err != nil {
		t.Fatalf("GetRoleMapping: %v", err)
	}
	if ok {
		t.Fatal("GetRoleMapping: want ok = false for a nonexistent id")
	}
}

func TestListRoleMappings(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if _, err := store.CreateRoleMapping(ctx, RoleMapping{
		IdentityKind: "spiffe", IdentityValue: "spiffe://grex.example/ns/default/sa/alice",
		Match: "exact", Role: "admin",
	}); err != nil {
		t.Fatalf("CreateRoleMapping: %v", err)
	}
	if _, err := store.CreateRoleMapping(ctx, RoleMapping{
		IdentityKind: "oidc_group", IdentityValue: "grex-viewers",
		Match: "exact", Role: "viewer",
	}); err != nil {
		t.Fatalf("CreateRoleMapping: %v", err)
	}

	mappings, err := store.ListRoleMappings(ctx)
	if err != nil {
		t.Fatalf("ListRoleMappings: %v", err)
	}
	if len(mappings) != 2 {
		t.Fatalf("ListRoleMappings returned %d rows, want 2", len(mappings))
	}
}

func TestRoleMappingContextCanceled(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.CreateRoleMapping(ctx, RoleMapping{
		IdentityKind: "spiffe", IdentityValue: "x", Match: "exact", Role: "viewer",
	}); err == nil {
		t.Fatal("CreateRoleMapping: want error for a cancelled context")
	}
	if _, _, err := store.GetRoleMapping(ctx, 1); err == nil {
		t.Fatal("GetRoleMapping: want error for a cancelled context")
	}
	if _, err := store.ListRoleMappings(ctx); err == nil {
		t.Fatal("ListRoleMappings: want error for a cancelled context")
	}
}
