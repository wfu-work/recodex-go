package workspace

import (
	"errors"
	"path/filepath"

	"recodex-go/internal/config"
)

var ErrWorkspaceNotAllowed = errors.New("workspace is not in allowlist")

type Registry struct {
	items []config.WorkspaceConfig
}

func NewRegistry(items []config.WorkspaceConfig) Registry {
	return Registry{items: append([]config.WorkspaceConfig(nil), items...)}
}

func (r Registry) List() []config.WorkspaceConfig {
	return append([]config.WorkspaceConfig(nil), r.items...)
}

func (r Registry) Resolve(value string) (config.WorkspaceConfig, error) {
	cleanValue, err := filepath.Abs(value)
	if err != nil {
		return config.WorkspaceConfig{}, err
	}
	cleanValue = filepath.Clean(cleanValue)

	for _, item := range r.items {
		if value == item.Name || cleanValue == item.Path {
			return item, nil
		}
	}
	return config.WorkspaceConfig{}, ErrWorkspaceNotAllowed
}
