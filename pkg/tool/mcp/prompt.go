package mcp

// Source: src/tools/ListMcpResourcesTool/prompt.ts
func listMcpResourcesPrompt() string {
	return "List available resources from configured MCP servers.\n" +
		"Each returned resource will include all standard MCP resource fields plus a 'server' field\n" +
		"indicating which server the resource belongs to.\n\n" +
		"Parameters:\n" +
		"- server (optional): The name of a specific MCP server to get resources from. If not provided,\n" +
		"  resources from all servers will be returned."
}

// Source: src/tools/ReadMcpResourceTool/prompt.ts
func readMcpResourcePrompt() string {
	return "Reads a specific resource from an MCP server, identified by server name and resource URI.\n\n" +
		"Parameters:\n" +
		"- server (required): The name of the MCP server from which to read the resource\n" +
		"- uri (required): The URI of the resource to read"
}
