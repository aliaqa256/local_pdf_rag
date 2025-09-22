#!/usr/bin/env -S node --enable-source-maps
import { StdioServerTransport } from "@modelcontextprotocol/sdk/transports/stdio.js";
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { z } from "zod";
import fetch from "node-fetch";

const RAG_BASE_URL = process.env.RAG_BASE_URL || "http://localhost:8080";

async function main() {
	const transport = new StdioServerTransport();
	const server = new Server(
		{
			name: "rag-mcp-server",
			version: "0.1.0",
			capabilities: {
				tools: {},
			},
		},
		transport
	);

	// Tool: query RAG
	server.tool(
		{
			name: "rag.query",
			description:
				"Ask a question against the local RAG knowledge base and get an answer with sources.",
			inputSchema: z.object({
				question: z.string().min(1),
			}),
		},
		async ({ question }) => {
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
	server.tool(
		{
			name: "rag.search_sources",
			description:
				"Find relevant source documents for a query, returning document ids, filenames, and snippets.",
			inputSchema: z.object({
				query: z.string().min(1),
			}),
		},
		async ({ query }) => {
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
	server.tool(
		{
			name: "rag.get_file",
			description:
				"Download a stored PDF by documentId and filename. Returns base64 content.",
			inputSchema: z.object({
				documentId: z.string().min(1),
				filename: z.string().min(1),
			}),
		},
		async ({ documentId, filename }) => {
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

	await server.connect();
}

main().catch((err) => {
	console.error("Fatal error starting rag-mcp-server:", err);
	process.exit(1);
});
