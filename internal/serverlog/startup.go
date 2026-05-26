package serverlog

import (
	"log"
	"net"
)

func BridgeStartup(addr string, pairingToken string) {
	baseURL := publicBaseURL(addr)
	log.Printf("Recodex Web 控制台: %s/", baseURL)
	log.Printf("Recodex API 地址: %s/api", baseURL)
	log.Printf("Recodex WebSocket 地址: %s/api/ws", wsBaseURL(addr))
	if pairingToken == "" {
		log.Printf("Recodex 配对状态: 已禁用或已过期")
		return
	}
	log.Printf("Recodex 配对 Token: %s", pairingToken)
}

func publicBaseURL(addr string) string {
	return "http://" + publicAddr(addr)
}

func wsBaseURL(addr string) string {
	return "ws://" + publicAddr(addr)
}

func publicAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
