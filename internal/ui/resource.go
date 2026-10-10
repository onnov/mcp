// Package ui embeds the repository picker shipped with the server binary.
package ui

import _ "embed"

// Hosts such as Claude and ChatGPT cache card HTML by URI, so a changed card
// gets a new URI. The previous URI stays served for connections whose cached
// tool descriptors still point to it; reconnect the connector to drop it.
const URI = "ui://ghf/repository-picker-v2.html"
const PreviousURI = "ui://ghf/repository-picker-v1.html"
const MIMEType = "text/html;profile=mcp-app"

//go:embed picker.html
var HTML string
