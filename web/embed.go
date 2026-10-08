// Package web embeds the dashboard static files.
//
// The pattern `*` always matches at least this file, so the build never
// breaks while index.html / app.js / style.css / vendor/ are being added.
// Directories matched by `*` (vendor/) are embedded recursively.
package web

import "embed"

//go:embed *
var FS embed.FS
