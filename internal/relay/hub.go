package relay

import (
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

type Hub struct {
	mu       sync.Mutex
	rooms    map[string]map[*peer]struct{}
	upgrader websocket.Upgrader
}

type peer struct {
	room string
	conn *websocket.Conn
	send chan []byte
}

func NewHub() *Hub {
	return &Hub{
		rooms: map[string]map[*peer]struct{}{},
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

func (h *Hub) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	room := strings.TrimPrefix(r.URL.Path, "/relay/")
	room = strings.Trim(room, "/")
	if room == "" {
		http.Error(w, "room is required", http.StatusBadRequest)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	p := &peer{room: room, conn: conn, send: make(chan []byte, 32)}
	h.add(p)
	defer h.remove(p)

	go p.writeLoop()
	p.readLoop(h)
}

func (h *Hub) add(p *peer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rooms[p.room] == nil {
		h.rooms[p.room] = map[*peer]struct{}{}
	}
	h.rooms[p.room][p] = struct{}{}
}

func (h *Hub) remove(p *peer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if peers, ok := h.rooms[p.room]; ok {
		delete(peers, p)
		if len(peers) == 0 {
			delete(h.rooms, p.room)
		}
	}
	close(p.send)
	_ = p.conn.Close()
}

func (h *Hub) broadcast(sender *peer, message []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for p := range h.rooms[sender.room] {
		if p == sender {
			continue
		}
		select {
		case p.send <- message:
		default:
		}
	}
}

func (p *peer) readLoop(h *Hub) {
	for {
		messageType, message, err := p.conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		h.broadcast(p, message)
	}
}

func (p *peer) writeLoop() {
	for message := range p.send {
		if err := p.conn.WriteMessage(websocket.TextMessage, message); err != nil {
			return
		}
	}
}
