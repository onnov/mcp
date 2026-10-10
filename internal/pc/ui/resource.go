// Package ui embeds the MCP Apps workspace and command confirmation cards.
package ui

import (
	_ "embed"
	"strings"
)

// Hosts such as Claude and ChatGPT cache card HTML by URI, so a changed card
// gets a new URI. The previous URI stays served for connections whose cached
// tool descriptors still point to it; refresh the connector to drop it.
const PickerURI = "ui://pc-mcp/workspace-v9.html"
const PreviousPickerURI = "ui://pc-mcp/workspace-v8.html"
const ApprovalURI = "ui://pc-mcp/approve-v13.html"
const PreviousApprovalURI = "ui://pc-mcp/approve-v12.html"
const MIMEType = "text/html;profile=mcp-app"

//go:embed picker.html
var Picker string

//go:embed approve.html
var Approval string

//go:embed bridge.js
var bridge string

func init() {
	Picker = strings.Replace(Picker, "/*BRIDGE*/", bridge, 1)
	Approval = strings.Replace(Approval, "/*BRIDGE*/", bridge, 1)
}
