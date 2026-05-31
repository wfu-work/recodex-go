package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesEmbeddedAssets(t *testing.T) {
	assets, err := loadStaticAssets()
	if err != nil {
		t.Fatalf("load static assets: %v", err)
	}

	handler := Handler()

	assertServesFile(t, handler, "/", assets.files["index.html"])
	assertServesFile(t, handler, "/settings", assets.files["settings.html"])
	assertServesFile(t, handler, "/unknown-route", assets.files["index.html"])

	jsPath := ""
	for name := range assets.files {
		if strings.HasSuffix(name, ".js") {
			jsPath = name
			break
		}
	}
	if jsPath == "" {
		t.Fatal("embedded web assets missing JavaScript chunk")
	}

	response := request(handler, "/"+jsPath)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /%s status = %d, want %d", jsPath, response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("GET /%s content-type = %q, want javascript", jsPath, response.Header().Get("Content-Type"))
	}
}

func assertServesFile(t *testing.T, handler http.Handler, target string, want []byte) {
	t.Helper()

	response := request(handler, target)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d", target, response.Code, http.StatusOK)
	}
	if got := response.Body.Bytes(); string(got) != string(want) {
		t.Fatalf("GET %s body length = %d, want %d", target, len(got), len(want))
	}
}

func request(handler http.Handler, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
