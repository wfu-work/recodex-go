package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"recodex-go/internal/codex"
)

const maxStoredEventBytes = 32 * 1024 * 1024

func (m *Manager) readEvents(id string) ([]codex.Event, error) {
	return m.readEventsPage(id, 1000)
}

func (m *Manager) readEventsPage(id string, limit int) ([]codex.Event, error) {
	events, err := readEventFile(m.eventsPath(id), limit)
	if errors.Is(err, os.ErrNotExist) {
		events, err = readEventFile(m.legacyEventsPath(id), limit)
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return compactLiveEvents(events), nil
}

func readEventFile(path string, limit int) ([]codex.Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	if strings.HasSuffix(path, ".json") {
		var events []codex.Event
		if err := json.NewDecoder(file).Decode(&events); err != nil {
			return nil, err
		}
		return tailEvents(events, limit), nil
	}

	var events []codex.Event
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxStoredEventBytes)
	for scanner.Scan() {
		var event codex.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		events = appendTailEvent(events, event, limit)
	}
	return events, scanner.Err()
}

func appendTailEvent(events []codex.Event, event codex.Event, limit int) []codex.Event {
	events = append(events, event)
	if limit <= 0 || len(events) <= limit*2 {
		return events
	}
	copy(events, events[len(events)-limit:])
	return events[:limit]
}

func tailEvents(events []codex.Event, limit int) []codex.Event {
	if limit <= 0 || len(events) <= limit {
		return events
	}
	return append([]codex.Event(nil), events[len(events)-limit:]...)
}

func compactLiveEvents(events []codex.Event) []codex.Event {
	if len(events) < 2 {
		return events
	}
	compacted := make([]codex.Event, 0, len(events))
	for _, event := range events {
		if len(compacted) > 0 && isLiveStatusEvent(event) && isLiveStatusEvent(compacted[len(compacted)-1]) {
			compacted[len(compacted)-1] = event
			continue
		}
		compacted = append(compacted, event)
	}
	return compacted
}
