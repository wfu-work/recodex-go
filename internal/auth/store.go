package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"recodex-go/internal/statefile"
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
	if !ok || key == "" || subtle.ConstantTimeCompare([]byte(device.Key), []byte(key)) != 1 {
		return false
	}
	previous := device
	device.LastSeen = time.Now()
	s.devices[id] = device
	if err := s.saveLocked(); err != nil {
		s.devices[id] = previous
		return false
	}
	return true
}

func (s *Store) IsAuthorized(id, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	device, ok := s.devices[id]
	return ok && key != "" && subtle.ConstantTimeCompare([]byte(device.Key), []byte(key)) == 1
}

func (s *Store) Pair(id, name string) (Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if len(id) > 128 {
		return Device{}, errors.New("device ID exceeds 128 byte limit")
	}
	if len(name) > 256 {
		return Device{}, errors.New("device name exceeds 256 byte limit")
	}
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
	previous, existed := s.devices[device.ID]
	s.devices[device.ID] = device
	if err := s.saveLocked(); err != nil {
		if existed {
			s.devices[device.ID] = previous
		} else {
			delete(s.devices, device.ID)
		}
		return Device{}, err
	}
	return device, nil
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
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastSeen.After(out[j].LastSeen)
	})
	return out
}

func (s *Store) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id = strings.TrimSpace(id)
	if _, ok := s.devices[id]; !ok {
		return errors.New("device not found")
	}
	previous := s.devices[id]
	delete(s.devices, id)
	if err := s.saveLocked(); err != nil {
		s.devices[id] = previous
		return err
	}
	return nil
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := json.Unmarshal(raw, &s.devices); err != nil {
		return err
	}
	if s.devices == nil {
		s.devices = map[string]Device{}
	}
	return nil
}

func (s *Store) saveLocked() error {
	raw, err := json.MarshalIndent(s.devices, "", "  ")
	if err != nil {
		return err
	}
	return statefile.WriteFile(s.path, raw, 0o600)
}

func RandomToken(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
