/*
Package server serves the production Dashboard from homelab-server.

本文件负责提供已构建的静态文件，并为客户端路由回退到 index.html。
本文件不构建前端、不处理 API 路由，也不允许通过该入口修改文件。

数据链路：browser GET -> SPA handler -> web/dist asset or index.html
*/
package server

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// NewSPAHandler serves a built frontend and falls back to index.html for
// client-side routes. API routes remain owned by the more-specific ServeMux
// registrations in cmd/server.
// NewSPAHandler 只接受 GET/HEAD，并将未知页面路径交给前端路由处理。
func NewSPAHandler(directory string) http.Handler {
	root := os.DirFS(directory)
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		cleaned := path.Clean("/" + r.URL.Path)
		name := strings.TrimPrefix(cleaned, "/")
		if name != "" {
			if info, err := fs.Stat(root, name); err == nil && !info.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
		}
		request := r.Clone(r.Context())
		request.URL.Path = "/"
		files.ServeHTTP(w, request)
	})
}
