# RAG MCP Server for Claude Desktop

This MCP server exposes your local RAG service to Claude Desktop as tools.

Tools:

- `rag.query(question)` – Ask a question against the RAG knowledge base.
- `rag.search_sources(query)` – Find relevant documents with snippets.
- `rag.get_file(documentId, filename)` – Download a stored PDF (base64).

## Prerequisites

- Node.js 18+
- Your Go RAG API running (default `http://localhost:8080`)

## Install

```bash
cd /home/aliaqa/Desktop/code/rag/mcp
npm install
npm run build
```

## Run locally

```bash
RAG_BASE_URL=http://localhost:8080 node dist/server.js
```

## Configure Claude Desktop

Add this to your Claude Desktop MCP config (JSON):

```json
{
	"mcpServers": {
		"rag": {
			"command": "/usr/bin/node",
			"args": ["/home/aliaqa/Desktop/code/rag/mcp/dist/server.js"],
			"env": {
				"RAG_BASE_URL": "http://localhost:8080"
			}
		}
	}
}
```

On Linux, the config file is typically at `~/.config/Claude/mcp.json`.

## Notes

- Ensure the Go server exposes `/query`, `/search-sources`, and `/files/:documentId/:filename`.
- You can change the base URL via `RAG_BASE_URL`.
