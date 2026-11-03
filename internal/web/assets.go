package web

import "embed"

// Files contains the complete browser application; no external assets are needed.
//
//go:embed index.html app.js tracking.js infrastructure.js styles.css login.css tracking.css infrastructure.css akron.png akron.svg login-background.svg
var Files embed.FS
