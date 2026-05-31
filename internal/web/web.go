package web

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed recodex-web.zip
var distZip []byte

func Handler() http.Handler {
	assets, err := loadStaticAssets()
	if err != nil {
		return http.NotFoundHandler()
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleanPath := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
		if cleanPath == "/" {
			assets.serveFile(w, r, "index.html")
			return
		}

		filePath := strings.TrimPrefix(cleanPath, "/")
		htmlPath := filePath + ".html"
		if assets.existsFile(htmlPath) {
			assets.serveFile(w, r, htmlPath)
			return
		}

		if assets.existsFile(filePath) {
			assets.serveFile(w, r, filePath)
			return
		}

		assets.serveFile(w, r, "index.html")
	})
}

type staticAssets struct {
	files map[string][]byte
}

func loadStaticAssets() (*staticAssets, error) {
	zipReader, err := zip.NewReader(bytes.NewReader(distZip), int64(len(distZip)))
	if err != nil {
		return nil, fmt.Errorf("load embedded web assets: %w", err)
	}

	files := make(map[string][]byte, len(zipReader.File))
	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() {
			continue
		}

		name := strings.TrimPrefix(path.Clean(file.Name), "./")
		if name == "" || strings.HasPrefix(name, "../") || !strings.HasPrefix(name, "recodex-web/") {
			continue
		}
		name = strings.TrimPrefix(name, "recodex-web/")

		open, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("open embedded web asset %q: %w", file.Name, err)
		}

		data, err := io.ReadAll(open)
		_ = open.Close()
		if err != nil {
			return nil, fmt.Errorf("read embedded web asset %q: %w", file.Name, err)
		}

		files[name] = data
	}

	if _, ok := files["index.html"]; !ok {
		return nil, fmt.Errorf("embedded web assets missing index.html")
	}

	return &staticAssets{files: files}, nil
}

func (assets *staticAssets) existsFile(name string) bool {
	if name == "" {
		return false
	}
	_, ok := assets.files[name]
	return ok
}

func (assets *staticAssets) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	data, ok := assets.files[name]
	if !ok {
		http.NotFound(w, r)
		return
	}

	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	http.ServeContent(w, r, path.Base(name), time.Time{}, bytes.NewReader(data))
}
