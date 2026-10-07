// Package ui embeds the MCP Apps workspace and command confirmation cards.
package ui

import (
	_ "embed"
	"strings"
)

const PickerURI = "ui://pc-mcp/workspace-v5.html"
const PreviousPickerURI = "ui://pc-mcp/workspace-v4.html"
const OlderPickerURI = "ui://pc-mcp/workspace-v3.html"
const OldestPickerURI = "ui://pc-mcp/workspace-v2.html"

// Keep serving these URIs for connections that cached older descriptors.
// Updating tool metadata in ChatGPT and restarting the server are independent.
const LegacyPickerURI = "ui://pc-mcp/workspace-v1.html"
const ApprovalURI = "ui://pc-mcp/approve-v8.html"
const PreviousApprovalURI = "ui://pc-mcp/approve-v7.html"
const OlderApprovalURI = "ui://pc-mcp/approve-v6.html"
const OldestApprovalURI = "ui://pc-mcp/approve-v5.html"
const EarlierApprovalURI = "ui://pc-mcp/approve-v4.html"
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
