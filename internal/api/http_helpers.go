package api

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"recodex-go/internal/config"
)

func writeJSON(w http.ResponseWriter, status int, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, `{"code":"response_encode_failed"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(raw, '\n'))
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" && !originAllowed(r, origin) {
			writeJSON(w, http.StatusForbidden, map[string]any{"code": "origin_denied", "message": "request origin is not allowed"})
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requestIsLoopback(r) {
			writeJSON(w, http.StatusForbidden, map[string]any{"code": "local_only", "message": "HTTP control APIs are available from the local machine only"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestIsLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func websocketOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	return origin == "" || originAllowed(r, origin)
}

func originAllowed(r *http.Request, rawOrigin string) bool {
	origin, err := url.Parse(rawOrigin)
	if err != nil || origin.Host == "" || origin.Scheme != "http" && origin.Scheme != "https" {
		return false
	}
	if strings.EqualFold(origin.Host, r.Host) {
		return true
	}
	originHost := origin.Hostname()
	originIP := net.ParseIP(originHost)
	originIsLoopback := originIP != nil && originIP.IsLoopback() || strings.EqualFold(originHost, "localhost")
	return requestIsLoopback(r) && originIsLoopback
}

func decodeRequest(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func listPagination(limitValue, offsetValue string) (int, int) {
	limit, _ := strconv.Atoi(limitValue)
	offset, _ := strconv.Atoi(offsetValue)
	return limit, offset
}

func pairingHost(_ *http.Request, cfg config.ServerConfig) string {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	if host == "0.0.0.0" || host == "::" || host == "[::]" {
		if detected := localIP(); detected != "" {
			host = detected
		} else {
			host = "127.0.0.1"
		}
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(cfg.Port))
}

func localIP() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	fallback := ""
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		addrs, err := networkInterface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP.IsLoopback() {
				continue
			}
			if ip := ipNet.IP.To4(); ip != nil {
				if ip.IsPrivate() {
					return ip.String()
				}
				if fallback == "" {
					fallback = ip.String()
				}
			}
		}
	}
	return fallback
}
