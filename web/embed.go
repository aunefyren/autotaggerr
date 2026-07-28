// Package web embeds the built single-page app so the whole UI ships inside the
// Go binary. The dist/ directory is produced by the Vite build (webui/ →
// ../web/dist); CI runs that build before `go build`.
package web

import "embed"

// Dist holds the compiled SPA assets (index.html + hashed JS/CSS under assets/).
//
//go:embed all:dist
var Dist embed.FS
