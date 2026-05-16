package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:recodex-web
var dist embed.FS

func Handler() http.Handler {
	staticFS, err := fs.Sub(dist, "recodex-web")
	if err != nil {
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(http.FS(staticFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleanPath := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
		if cleanPath == "/" {
			serveIndex(w, r, staticFS)
			return
		}

		htmlPath := strings.TrimPrefix(cleanPath, "/") + ".html"
		if existsFile(staticFS, htmlPath) {
			http.ServeFileFS(w, r, staticFS, htmlPath)
			return
		}

		if existsFile(staticFS, strings.TrimPrefix(cleanPath, "/")) {
			fileServer.ServeHTTP(w, r)
			return
		}

		serveIndex(w, r, staticFS)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, staticFS fs.FS) {
	http.ServeFileFS(w, r, staticFS, "index.html")
}

func existsFile(staticFS fs.FS, name string) bool {
	if name == "" {
		return false
	}
	info, err := fs.Stat(staticFS, name)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
