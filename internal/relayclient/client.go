package relayclient

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"recodex-go/internal/config"
	"recodex-go/internal/version"
)

type Handler func(context.Context, *websocket.Conn)

type Client struct {
	cfg     config.RelayConfig
	handler Handler
	dialer  *websocket.Dialer
}

func New(cfg config.RelayConfig, handler Handler) (*Client, error) {
	if !cfg.Enabled {
		return nil, errors.New("relay is disabled")
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("relay.url is required")
	}
	if strings.TrimSpace(cfg.RoomID) == "" {
		return nil, errors.New("relay.room_id is required")
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
		return nil, errors.New("relay.client_id is required")
	}
	if strings.TrimSpace(cfg.ClientSecret) == "" {
		return nil, errors.New("relay.client_secret is required")
	}
	if strings.TrimSpace(cfg.ClientType) == "" {
		cfg.ClientType = "bridge"
	}
	if cfg.ReconnectSeconds <= 0 {
		cfg.ReconnectSeconds = 5
	}
	return &Client{
		cfg:     cfg,
		handler: handler,
		dialer:  websocket.DefaultDialer,
	}, nil
}

func (c *Client) Run(ctx context.Context) {
	delay := time.Duration(c.cfg.ReconnectSeconds) * time.Second
	for {
		if err := c.connectOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("Recodex Relay 连接失败: %v", err)
		}
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func (c *Client) connectOnce(ctx context.Context) error {
	wsURL, err := SignedURL(c.cfg, time.Now())
	if err != nil {
		return err
	}
	conn, _, err := c.dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return err
	}
	log.Printf("Recodex Bridge 已连接 Relay 房间: %s", c.cfg.RoomID)
	c.handler(ctx, conn)
	log.Printf("Recodex Bridge Relay 连接已断开: %s", c.cfg.RoomID)
	return nil
}

func SignedURL(cfg config.RelayConfig, now time.Time) (string, error) {
	base, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil {
		return "", err
	}
	if base.Scheme != "ws" && base.Scheme != "wss" {
		return "", fmt.Errorf("relay.url must use ws or wss scheme, got %q", base.Scheme)
	}
	roomID := strings.Trim(strings.TrimSpace(cfg.RoomID), "/")
	if roomID == "" {
		return "", errors.New("relay.room_id is required")
	}
	clientID := strings.TrimSpace(cfg.ClientID)
	clientType := strings.TrimSpace(cfg.ClientType)
	clientSecret := strings.TrimSpace(cfg.ClientSecret)
	if clientType == "" {
		clientType = "bridge"
	}
	if clientID == "" || clientSecret == "" {
		return "", errors.New("relay.client_id and relay.client_secret are required")
	}

	timestamp := fmt.Sprintf("%d", now.Unix())
	nonce, err := randomNonce()
	if err != nil {
		return "", err
	}
	signature := Sign(clientSecret, clientID, clientType, roomID, timestamp, nonce)

	base.Path = strings.TrimRight(base.Path, "/") + "/" + url.PathEscape(roomID)
	query := base.Query()
	query.Set("clientId", clientID)
	query.Set("clientType", clientType)
	query.Set("timestamp", timestamp)
	query.Set("nonce", nonce)
	query.Set("signature", signature)
	query.Set("platform", clientPlatform())
	query.Set("version", clientVersion())
	if target := strings.TrimSpace(cfg.TargetClientID); target != "" {
		query.Set("targetClientId", target)
	}
	if roomToken := strings.TrimSpace(cfg.RoomToken); roomToken != "" {
		query.Set("roomToken", roomToken)
	}
	base.RawQuery = query.Encode()
	return base.String(), nil
}

func clientPlatform() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

func clientVersion() string {
	return "recodex-go/" + version.Value
}

func Sign(secret, clientID, clientType, roomID, timestamp, nonce string) string {
	payload := strings.Join([]string{clientID, clientType, roomID, timestamp, nonce}, "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func PublicConfig(cfg config.RelayConfig) map[string]any {
	if !cfg.Enabled {
		return map[string]any{"enabled": false}
	}
	return map[string]any{
		"enabled":        true,
		"url":            publicURL(cfg),
		"roomId":         strings.TrimSpace(cfg.RoomID),
		"clientId":       strings.TrimSpace(cfg.ClientID),
		"clientType":     strings.TrimSpace(cfg.ClientType),
		"targetClientId": strings.TrimSpace(cfg.TargetClientID),
	}
}

func publicURL(cfg config.RelayConfig) string {
	if value := strings.TrimSpace(cfg.PublicURL); value != "" {
		return value
	}
	return strings.TrimSpace(cfg.URL)
}

func randomNonce() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var b strings.Builder
	b.Grow(24)
	max := big.NewInt(int64(len(alphabet)))
	for i := 0; i < 24; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(alphabet[int(n.Int64())])
	}
	return b.String(), nil
}
