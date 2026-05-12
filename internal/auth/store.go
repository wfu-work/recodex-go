package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"createdAt"`
	LastSeen  time.Time `json:"lastSeen"`
}

type PublicDevice struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	LastSeen  time.Time `json:"lastSeen"`
}

type Store struct {
	mu      sync.Mutex
	path    string
	devices map[string]Device
}

func NewStore(stateDir string) (*Store, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	store := &Store{
		path:    filepath.Join(stateDir, "devices.json"),
		devices: map[string]Device{},
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Verify(id, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	device, ok := s.devices[id]
	if !ok || device.Key != key || key == "" {
		return false
	}
	device.LastSeen = time.Now()
	s.devices[id] = device
	_ = s.saveLocked()
	return true
}

func (s *Store) Pair(id, name string) (Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		id = "dev_" + RandomToken(9)
	}
	now := time.Now()
	device := Device{
		ID:        id,
		Name:      name,
		Key:       RandomToken(32),
		CreatedAt: now,
		LastSeen:  now,
	}
	if device.Name == "" {
		device.Name = "Mobile Device"
	}
	s.devices[device.ID] = device
	return device, s.saveLocked()
}

func (s *Store) Devices() []PublicDevice {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PublicDevice, 0, len(s.devices))
	for _, device := range s.devices {
		out = append(out, PublicDevice{
			ID:        device.ID,
			Name:      device.Name,
			CreatedAt: device.CreatedAt,
			LastSeen:  device.LastSeen,
		})
	}
	return out
}

func (s *Store) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.devices[id]; !ok {
		return errors.New("device not found")
	}
	delete(s.devices, id)
	return s.saveLocked()
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return json.Unmarshal(raw, &s.devices)
}

func (s *Store) saveLocked() error {
	raw, err := json.MarshalIndent(s.devices, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0o600)
}

func RandomToken(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
