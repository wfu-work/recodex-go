package workspace

import (
	"path/filepath"
	"testing"

	"recodex-go/internal/config"
)

func TestResolveAllowsOnlyNamedOrExactWorkspace(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "app")
	registry := NewRegistry([]config.WorkspaceConfig{{Name: "app", Path: allowed}})

	if _, err := registry.Resolve("app"); err != nil {
		t.Fatalf("resolve by name failed: %v", err)
	}
	if _, err := registry.Resolve(allowed); err != nil {
		t.Fatalf("resolve by path failed: %v", err)
	}
	if _, err := registry.Resolve(filepath.Join(root, "other")); err != ErrWorkspaceNotAllowed {
		t.Fatalf("expected ErrWorkspaceNotAllowed, got %v", err)
	}
}
