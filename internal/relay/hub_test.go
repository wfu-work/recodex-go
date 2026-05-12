package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRelayForwardsOpaqueMessagesInsideRoom(t *testing.T) {
	hub := NewHub()
	server := httptest.NewServer(httpHandler(hub))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/relay/test-room"
	left, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial left: %v", err)
	}
	defer left.Close()
	right, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial right: %v", err)
	}
	defer right.Close()

	payload := []byte(`{"encrypted":"opaque"}`)
	if err := left.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = right.SetReadDeadline(time.Now().Add(time.Second))
	_, got, err := right.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("unexpected payload: %s", got)
	}
}

func httpHandler(hub *Hub) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/relay/", hub.HandleWebSocket)
	return mux
}
