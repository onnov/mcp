// Package ui embeds the MCP Apps workspace and command confirmation cards.
package ui

import (
	_ "embed"
	"strings"
)

const PickerURI = "ui://pc-mcp/workspace-v3.html"
const PreviousPickerURI = "ui://pc-mcp/workspace-v2.html"

// Keep serving this URI for connections that cached the original descriptor.
// Updating tool metadata in ChatGPT and restarting the server are independent.
const LegacyPickerURI = "ui://pc-mcp/workspace-v1.html"
const ApprovalURI = "ui://pc-mcp/approve-v2.html"
const LegacyApprovalURI = "ui://pc-mcp/approve-v1.html"
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
