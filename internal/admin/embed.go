package admin

import "embed"

// WebFS contains the embedded web assets (templates and static files).
// The filesystem root is "web/", so paths are like:
//   - web/templates/dashboard.html
//   - web/static/style.css
//
// Use io/fs.Sub(WebFS, "web") to get the templates/ and static/ dirs at root.
//
//go:embed web/templates/*.html web/static/*.css
var WebFS embed.FS
