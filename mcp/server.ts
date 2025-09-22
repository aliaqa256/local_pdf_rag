#!/usr/bin/env -S node --enable-source-maps
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { z } from "zod";
import fetch from "node-fetch";
import mysql from "mysql2/promise";

const RAG_BASE_URL = process.env.RAG_BASE_URL || "http://localhost:8090";

// MySQL env
const MYSQL_HOST = process.env.MYSQL_HOST || "localhost";
const MYSQL_PORT = parseInt(process.env.MYSQL_PORT || "3306", 10);
const MYSQL_USER = process.env.MYSQL_USER || "rag_user";
const MYSQL_PASSWORD = process.env.MYSQL_PASSWORD || "rag_password";
const MYSQL_DATABASE = process.env.MYSQL_DATABASE || "rag_db";

// Qdrant env
const QDRANT_HOST = process.env.QDRANT_HOST || "localhost";
const QDRANT_PORT = process.env.QDRANT_PORT || "6333";
const QDRANT_BASE_URL =
	process.env.QDRANT_BASE_URL || `http://${QDRANT_HOST}:${QDRANT_PORT}`;

async function main() {
	const mcpServer = new McpServer({
		name: "rag-mcp-server",
		version: "0.1.0",
	});

	// Create a lazy MySQL pool
	let mysqlPool: mysql.Pool | null = null;
	async function getMysqlPool() {
		if (!mysqlPool) {
			mysqlPool = mysql.createPool({
				host: MYSQL_HOST,
				port: MYSQL_PORT,
				user: MYSQL_USER,
				password: MYSQL_PASSWORD,
				database: MYSQL_DATABASE,
				connectionLimit: 5,
			});
		}
		return mysqlPool;
	}

	// Tool: query RAG
	mcpServer.registerTool(
		"rag_query",
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
		"rag_search_sources",
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

	// Tool: MySQL list documents (direct DB access)
	mcpServer.registerTool(
		"mysql_list_documents",
		{
			description: "List documents from the RAG MySQL database.",
			inputSchema: {
				limit: z.number().int().min(1).max(200).optional(),
				offset: z.number().int().min(0).optional(),
			},
		},
		async ({ limit = 50, offset = 0 }: { limit?: number; offset?: number }) => {
			const pool = await getMysqlPool();
			const [rows] = await pool.query(
				`SELECT id, original_filename, status, chunk_count, created_at, updated_at FROM documents ORDER BY created_at DESC LIMIT ? OFFSET ?`,
				[limit, offset]
			);
			return {
				content: [{ type: "text", text: JSON.stringify(rows, null, 2) }],
			};
		}
	);

	// Tool: MySQL search chunks using simple LIKE matching (same spirit as service scoring)
	mcpServer.registerTool(
		"mysql_search_chunks",
		{
			description:
				"Search chunk texts in MySQL using keyword matching. Returns chunk and document info.",
			inputSchema: {
				query: z.string().min(1),
				limit: z.number().int().min(1).max(200).optional(),
				offset: z.number().int().min(0).optional(),
			},
		},
		async ({
			query,
			limit = 20,
			offset = 0,
		}: {
			query: string;
			limit?: number;
			offset?: number;
		}) => {
			const tokens = query
				.toLowerCase()
				.split(/\s+/)
				.filter((t) => t.length > 0)
				.slice(0, 8);
			if (tokens.length === 0) {
				return { content: [{ type: "text", text: "[]" }] };
			}
			const likeClauses = tokens
				.map(() => `dc.chunk_text LIKE ?`)
				.join(" AND ");
			const likeValues = tokens.map((t) => `%${t}%`);
			const sql = `
				SELECT dc.id AS chunk_id, dc.document_id, dc.page_number, dc.chunk_index, dc.word_count,
				       SUBSTRING(dc.chunk_text, 1, 800) AS snippet,
				       d.original_filename
				FROM document_chunks dc
				JOIN documents d ON d.id = dc.document_id
				WHERE ${likeClauses}
				ORDER BY dc.created_at DESC
				LIMIT ? OFFSET ?`;
			const pool = await getMysqlPool();
			const [rows] = await pool.query(sql, [...likeValues, limit, offset]);
			return {
				content: [{ type: "text", text: JSON.stringify(rows, null, 2) }],
			};
		}
	);

	// Qdrant: list collections
	mcpServer.registerTool(
		"qdrant_list_collections",
		{
			description: "List Qdrant collections.",
			inputSchema: {},
		},
		async () => {
			const res = await fetch(`${QDRANT_BASE_URL}/collections`);
			if (!res.ok) {
				throw new Error(`Qdrant error: ${res.status} ${await res.text()}`);
			}
			const data = await res.json();
			return {
				content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
			};
		}
	);

	// Qdrant: search by vector
	mcpServer.registerTool(
		"qdrant_search_by_vector",
		{
			description:
				"Search a Qdrant collection by vector. Provide collection name, vector array, and limit.",
			inputSchema: {
				collection: z.string().min(1),
				vector: z.array(z.number()).min(1),
				limit: z.number().int().min(1).max(100).optional(),
			},
		},
		async ({
			collection,
			vector,
			limit = 5,
		}: {
			collection: string;
			vector: number[];
			limit?: number;
		}) => {
			const res = await fetch(
				`${QDRANT_BASE_URL}/collections/${encodeURIComponent(
					collection
				)}/points/search`,
				{
					method: "POST",
					headers: { "Content-Type": "application/json" },
					body: JSON.stringify({ vector, limit }),
				}
			);
			if (!res.ok) {
				throw new Error(`Qdrant error: ${res.status} ${await res.text()}`);
			}
			const data = await res.json();
			return {
				content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
			};
		}
	);

	// Qdrant: scroll points with optional raw filter
	mcpServer.registerTool(
		"qdrant_scroll",
		{
			description:
				"Scroll points in a Qdrant collection. Optionally pass a raw filter JSON string and limit.",
			inputSchema: {
				collection: z.string().min(1),
				filterJson: z.string().optional(),
				limit: z.number().int().min(1).max(200).optional(),
			},
		},
		async ({
			collection,
			filterJson,
			limit = 100,
		}: {
			collection: string;
			filterJson?: string;
			limit?: number;
		}) => {
			let filter: any = undefined;
			if (filterJson) {
				try {
					filter = JSON.parse(filterJson);
				} catch (e) {
					throw new Error(`Invalid filterJson: ${(e as Error).message}`);
				}
			}
			const res = await fetch(
				`${QDRANT_BASE_URL}/collections/${encodeURIComponent(
					collection
				)}/points/scroll`,
				{
					method: "POST",
					headers: { "Content-Type": "application/json" },
					body: JSON.stringify({ filter, limit }),
				}
			);
			if (!res.ok) {
				throw new Error(`Qdrant error: ${res.status} ${await res.text()}`);
			}
			const data = await res.json();
			return {
				content: [{ type: "text", text: JSON.stringify(data, null, 2) }],
			};
		}
	);

	// Tool: get file (download PDF by document id and filename)
	mcpServer.registerTool(
		"rag_get_file",
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
