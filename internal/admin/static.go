package admin

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
)

//go:embed static/admin.css
var adminCSS []byte

// cssPath is the only static route; cssHref carries the content hash so a changed file is a new URL.
const cssPath = "/static/admin.css"

var cssHref = func() string {
	sum := sha256.Sum256(adminCSS)
	return cssPath + "?v=" + hex.EncodeToString(sum[:8])
}()

// stylesheet serves the embedded CSS. Only the URL with the current hash is cached long term.
func stylesheet(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/css; charset=utf-8")
	if r.URL.RequestURI() == cssHref {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	_, _ = w.Write(adminCSS)
}
