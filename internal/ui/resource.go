// Package ui embeds the repository picker shipped with the server binary.
package ui

import _ "embed"

const URI = "ui://ghf/repository-picker-v1.html"
const MIMEType = "text/html;profile=mcp-app"

//go:embed picker.html
var HTML string
