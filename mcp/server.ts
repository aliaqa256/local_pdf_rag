#!/usr/bin/env -S node --enable-source-maps
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { z } from "zod";
import fetch from "node-fetch";
import mysql from "mysql2/promise";
import { Client as MinioClient } from "minio";

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

// MinIO env
const MINIO_ENDPOINT = process.env.MINIO_ENDPOINT || "localhost:9000";
const MINIO_ACCESS_KEY = process.env.MINIO_ACCESS_KEY || "minioadmin";
const MINIO_SECRET_KEY = process.env.MINIO_SECRET_KEY || "minioadmin123";
const MINIO_USE_SSL =
	(process.env.MINIO_USE_SSL || "false").toLowerCase() === "true";
const MINIO_DEFAULT_BUCKET = process.env.MINIO_DEFAULT_BUCKET || "documents";

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

	// Create a lazy MinIO client
	let minioClient: MinioClient | null = null;
	function getMinioClient() {
		if (!minioClient) {
			const [endHost, endPortStr] = MINIO_ENDPOINT.split(":");
			const endPort = parseInt(
				endPortStr || (MINIO_USE_SSL ? "443" : "9000"),
				10
			);
			minioClient = new MinioClient({
				endPoint: endHost,
				port: endPort,
				useSSL: MINIO_USE_SSL,
				accessKey: MINIO_ACCESS_KEY,
				secretKey: MINIO_SECRET_KEY,
			});
		}
		return minioClient;
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

	// Tool: MySQL arbitrary SQL (read/write). Use with caution.
	mcpServer.registerTool(
		"mysql_execute",
		{
			description:
				"Execute an arbitrary SQL statement with optional JSON params array.",
			inputSchema: {
				sql: z.string().min(1),
				paramsJson: z.string().optional(),
			},
		},
		async ({ sql, paramsJson }: { sql: string; paramsJson?: string }) => {
			const pool = await getMysqlPool();
			let params: any[] = [];
			if (paramsJson) {
				try {
					params = JSON.parse(paramsJson);
				} catch (e) {
					throw new Error(`Invalid paramsJson: ${(e as Error).message}`);
				}
				if (!Array.isArray(params))
					throw new Error("paramsJson must be a JSON array");
			}
			const [rows] = await pool.query(sql, params as any);
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

	// Qdrant: create collection
	mcpServer.registerTool(
		"qdrant_create_collection",
		{
			description:
				"Create a Qdrant collection. schemaJson is raw JSON body for /collections/{name}.",
			inputSchema: {
				name: z.string().min(1),
				schemaJson: z.string().min(2),
			},
		},
		async ({ name, schemaJson }: { name: string; schemaJson: string }) => {
			let body: any;
			try {
				body = JSON.parse(schemaJson);
			} catch (e) {
				throw new Error(`Invalid schemaJson: ${(e as Error).message}`);
			}
			const res = await fetch(
				`${QDRANT_BASE_URL}/collections/${encodeURIComponent(name)}`,
				{
					method: "PUT",
					headers: { "Content-Type": "application/json" },
					body: JSON.stringify(body),
				}
			);
			if (!res.ok) {
				throw new Error(`Qdrant error: ${res.status} ${await res.text()}`);
			}
			return { content: [{ type: "text", text: await res.text() }] };
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

	// Qdrant: upsert points
	mcpServer.registerTool(
		"qdrant_upsert_points",
		{
			description:
				"Upsert points in a Qdrant collection. Provide raw JSON for points as per API.",
			inputSchema: {
				collection: z.string().min(1),
				pointsJson: z.string().min(2),
				wait: z.boolean().optional(),
			},
		},
		async ({
			collection,
			pointsJson,
			wait = true,
		}: {
			collection: string;
			pointsJson: string;
			wait?: boolean;
		}) => {
			let body: any;
			try {
				body = JSON.parse(pointsJson);
			} catch (e) {
				throw new Error(`Invalid pointsJson: ${(e as Error).message}`);
			}
			const url = `${QDRANT_BASE_URL}/collections/${encodeURIComponent(
				collection
			)}/points?wait=${wait ? "true" : "false"}`;
			const res = await fetch(url, {
				method: "PUT",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify(body),
			});
			if (!res.ok)
				throw new Error(`Qdrant error: ${res.status} ${await res.text()}`);
			return { content: [{ type: "text", text: await res.text() }] };
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

	// Qdrant: delete points by filter or ids
	mcpServer.registerTool(
		"qdrant_delete_points",
		{
			description:
				"Delete points from a collection. Provide raw JSON body for /points/delete (ids or filter).",
			inputSchema: {
				collection: z.string().min(1),
				deleteJson: z.string().min(2),
				wait: z.boolean().optional(),
			},
		},
		async ({
			collection,
			deleteJson,
			wait = true,
		}: {
			collection: string;
			deleteJson: string;
			wait?: boolean;
		}) => {
			let body: any;
			try {
				body = JSON.parse(deleteJson);
			} catch (e) {
				throw new Error(`Invalid deleteJson: ${(e as Error).message}`);
			}
			const url = `${QDRANT_BASE_URL}/collections/${encodeURIComponent(
				collection
			)}/points/delete?wait=${wait ? "true" : "false"}`;
			const res = await fetch(url, {
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify(body),
			});
			if (!res.ok)
				throw new Error(`Qdrant error: ${res.status} ${await res.text()}`);
			return { content: [{ type: "text", text: await res.text() }] };
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

	// MinIO: list buckets
	mcpServer.registerTool(
		"minio_list_buckets",
		{
			description: "List MinIO buckets.",
			inputSchema: {},
		},
		async () => {
			const client = getMinioClient();
			const buckets = await client.listBuckets();
			return {
				content: [{ type: "text", text: JSON.stringify(buckets, null, 2) }],
			};
		}
	);

	// MinIO: list objects in a bucket (recursive)
	mcpServer.registerTool(
		"minio_list_objects",
		{
			description:
				"List objects in a MinIO bucket. Optionally prefix and recursive.",
			inputSchema: {
				bucket: z.string().min(1).default(MINIO_DEFAULT_BUCKET),
				prefix: z.string().optional(),
				recursive: z.boolean().optional(),
			},
		},
		async ({
			bucket = MINIO_DEFAULT_BUCKET,
			prefix = "",
			recursive = true,
		}: {
			bucket?: string;
			prefix?: string;
			recursive?: boolean;
		}) => {
			const client = getMinioClient();
			const stream = client.listObjectsV2(bucket, prefix, recursive);
			const items: any[] = [];
			await new Promise<void>((resolve, reject) => {
				stream.on("data", (obj: any) => items.push(obj));
				stream.on("error", (e: any) => reject(e));
				stream.on("end", () => resolve());
			});
			return {
				content: [{ type: "text", text: JSON.stringify(items, null, 2) }],
			};
		}
	);

	// MinIO: get object -> base64
	mcpServer.registerTool(
		"minio_get_object",
		{
			description: "Fetch an object from MinIO and return base64 content.",
			inputSchema: {
				bucket: z.string().min(1).default(MINIO_DEFAULT_BUCKET),
				key: z.string().min(1),
			},
		},
		async ({
			bucket = MINIO_DEFAULT_BUCKET,
			key,
		}: {
			bucket?: string;
			key: string;
		}) => {
			const client = getMinioClient();
			const stream = await client.getObject(bucket, key);
			const chunks: Buffer[] = [];
			await new Promise<void>((resolve, reject) => {
				stream.on("data", (d: any) =>
					chunks.push(Buffer.isBuffer(d) ? d : Buffer.from(d))
				);
				stream.on("error", (e: any) => reject(e));
				stream.on("end", () => resolve());
			});
			const base64 = Buffer.concat(chunks).toString("base64");
			return {
				content: [
					{
						type: "text",
						text: JSON.stringify({ bucket, key, base64 }, null, 2),
					},
				],
			};
		}
	);

	// MinIO: put object from base64
	mcpServer.registerTool(
		"minio_put_object",
		{
			description: "Upload an object to MinIO from base64 string.",
			inputSchema: {
				bucket: z.string().min(1).default(MINIO_DEFAULT_BUCKET),
				key: z.string().min(1),
				base64: z.string().min(1),
				contentType: z.string().optional(),
			},
		},
		async ({
			bucket = MINIO_DEFAULT_BUCKET,
			key,
			base64,
			contentType = "application/octet-stream",
		}: {
			bucket?: string;
			key: string;
			base64: string;
			contentType?: string;
		}) => {
			const client = getMinioClient();
			const buffer = Buffer.from(base64, "base64");
			await client.putObject(bucket, key, buffer, {
				"Content-Type": contentType,
			} as any);
			return {
				content: [
					{
						type: "text",
						text: JSON.stringify({ ok: true, bucket, key }, null, 2),
					},
				],
			};
		}
	);

	// MinIO: remove object
	mcpServer.registerTool(
		"minio_remove_object",
		{
			description: "Remove a single object from MinIO.",
			inputSchema: {
				bucket: z.string().min(1).default(MINIO_DEFAULT_BUCKET),
				key: z.string().min(1),
			},
		},
		async ({
			bucket = MINIO_DEFAULT_BUCKET,
			key,
		}: {
			bucket?: string;
			key: string;
		}) => {
			const client = getMinioClient();
			await client.removeObject(bucket, key);
			return {
				content: [
					{
						type: "text",
						text: JSON.stringify({ ok: true, bucket, key }, null, 2),
					},
				],
			};
		}
	);

	// MinIO: create bucket
	mcpServer.registerTool(
		"minio_make_bucket",
		{
			description: "Create a MinIO bucket.",
			inputSchema: { bucket: z.string().min(1) },
		},
		async ({ bucket }: { bucket: string }) => {
			const client = getMinioClient();
			await client.makeBucket(bucket);
			return {
				content: [
					{ type: "text", text: JSON.stringify({ ok: true, bucket }, null, 2) },
				],
			};
		}
	);

	// MinIO: remove bucket
	mcpServer.registerTool(
		"minio_remove_bucket",
		{
			description: "Remove a MinIO bucket (must be empty).",
			inputSchema: { bucket: z.string().min(1) },
		},
		async ({ bucket }: { bucket: string }) => {
			const client = getMinioClient();
			await client.removeBucket(bucket);
			return {
				content: [
					{ type: "text", text: JSON.stringify({ ok: true, bucket }, null, 2) },
				],
			};
		}
	);

	// MinIO: presigned URLs
	mcpServer.registerTool(
		"minio_presigned_get",
		{
			description: "Generate a presigned GET URL for an object (seconds).",
			inputSchema: {
				bucket: z.string().min(1).default(MINIO_DEFAULT_BUCKET),
				key: z.string().min(1),
				expiresSeconds: z
					.number()
					.int()
					.min(1)
					.max(7 * 24 * 3600)
					.optional(),
			},
		},
		async ({
			bucket = MINIO_DEFAULT_BUCKET,
			key,
			expiresSeconds = 3600,
		}: {
			bucket?: string;
			key: string;
			expiresSeconds?: number;
		}) => {
			const client = getMinioClient();
			const url = await client.presignedGetObject(bucket, key, expiresSeconds);
			return {
				content: [{ type: "text", text: JSON.stringify({ url }, null, 2) }],
			};
		}
	);
	mcpServer.registerTool(
		"minio_presigned_put",
		{
			description: "Generate a presigned PUT URL for an object (seconds).",
			inputSchema: {
				bucket: z.string().min(1).default(MINIO_DEFAULT_BUCKET),
				key: z.string().min(1),
				expiresSeconds: z
					.number()
					.int()
					.min(1)
					.max(7 * 24 * 3600)
					.optional(),
			},
		},
		async ({
			bucket = MINIO_DEFAULT_BUCKET,
			key,
			expiresSeconds = 3600,
		}: {
			bucket?: string;
			key: string;
			expiresSeconds?: number;
		}) => {
			const client = getMinioClient();
			const url = await client.presignedPutObject(bucket, key, expiresSeconds);
			return {
				content: [{ type: "text", text: JSON.stringify({ url }, null, 2) }],
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
