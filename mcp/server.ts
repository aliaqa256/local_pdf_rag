#!/usr/bin/env -S node --enable-source-maps
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { z } from "zod";
import fetch from "node-fetch";

const RAG_BASE_URL = process.env.RAG_BASE_URL || "http://localhost:8080";

async function main() {
	const mcpServer = new McpServer({
		name: "rag-mcp-server",
		version: "0.1.0",
	});

	// Tool: query RAG
	mcpServer.registerTool(
		"rag.query",
		{
			description:
				"Ask a question against the local RAG knowledge base and get an answer with sources.",
			inputSchema: { question: z.string().min(1) },
		},
		async ({ question }: { question: string }) => {
			const res = await fetch(`${RAG_BASE_URL}/query`, {
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({ question }),
			});
			if (!res.ok) {
				const text = await res.text();
				throw new Error(`RAG query failed: ${res.status} ${text}`);
			}
			const data = await res.json();
			return {
				content: [
					{
						type: "text",
						text: JSON.stringify(data, null, 2),
					},
				],
			};
		}
	);

	// Tool: search sources
	mcpServer.registerTool(
		"rag.search_sources",
		{
			description:
				"Find relevant source documents for a query, returning document ids, filenames, and snippets.",
			inputSchema: { query: z.string().min(1) },
		},
		async ({ query }: { query: string }) => {
			const res = await fetch(`${RAG_BASE_URL}/search-sources`, {
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({ query }),
			});
			if (!res.ok) {
				const text = await res.text();
				throw new Error(`RAG search failed: ${res.status} ${text}`);
			}
			const data = await res.json();
			return {
				content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
			};
		}
	);

	// Tool: get file (download PDF by document id and filename)
	mcpServer.registerTool(
		"rag.get_file",
		{
			description:
				"Download a stored PDF by documentId and filename. Returns base64 content.",
			inputSchema: {
				documentId: z.string().min(1),
				filename: z.string().min(1),
			},
		},
		async ({
			documentId,
			filename,
		}: {
			documentId: string;
			filename: string;
		}) => {
			const url = `${RAG_BASE_URL}/files/${encodeURIComponent(
				documentId
			)}/${encodeURIComponent(filename)}`;
			const res = await fetch(url);
			if (!res.ok) {
				const text = await res.text();
				throw new Error(`RAG file download failed: ${res.status} ${text}`);
			}
			const buffer = Buffer.from(await res.arrayBuffer());
			const base64 = buffer.toString("base64");
			return {
				content: [
					{
						type: "text",
						text: JSON.stringify({
							documentId,
							filename,
							mimeType: "application/pdf",
							base64,
						}),
					},
				],
			};
		}
	);

	const transport = new StdioServerTransport();
	await mcpServer.connect(transport);
}

main().catch((err) => {
	console.error("Fatal error starting rag-mcp-server:", err);
	process.exit(1);
});
