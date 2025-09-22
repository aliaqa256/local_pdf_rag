package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"rag-service/internal/infrastructure/adapters"
	"rag-service/internal/infrastructure/config"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
)

func main() {
	// Load configuration
	cfg := config.Load()

	// Initialize adapters
	mysqlAdapter, err := adapters.NewMySQLAdapter(cfg)
	if err != nil {
		log.Fatalf("Failed to connect to MySQL: %v", err)
	}
	defer mysqlAdapter.Close()

	minioAdapter, err := adapters.NewMinIOAdapter(cfg)
	if err != nil {
		log.Fatalf("Failed to connect to MinIO: %v", err)
	}

	// Initialize LLM provider (optional)
	var llm adapters.LLMClient
	var modelName string
	if strings.ToLower(cfg.LLMProvider) == "google" {
		googleAdapter, err := adapters.NewGoogleGeminiAdapter(cfg)
		if err != nil {
			log.Fatalf("Failed to initialize Google Gemini: %v", err)
		}
		llm = googleAdapter
		modelName = cfg.GoogleModel
	} else if strings.ToLower(cfg.LLMProvider) == "ollama" {
		ollamaAdapter, err := adapters.NewOllamaAdapter(cfg)
		if err != nil {
			log.Fatalf("Failed to connect to Ollama: %v", err)
		}
		defer ollamaAdapter.Close()
		llm = ollamaAdapter
		modelName = cfg.OllamaModel
	} else {
		// LLM disabled (retrieval-only)
		llm = adapters.LLMClient(nil)
		modelName = "none"
	}

	// Initialize simple RAG service (without vector search for now)
	ragService := adapters.NewSimpleRAGService(llm, minioAdapter, mysqlAdapter, cfg)

	// Initialize database schema
	err = ragService.DatabaseSchema.CreateTables()
	if err != nil {
		log.Fatalf("Failed to create database tables: %v", err)
	}

	// Create a new Fiber instance
	app := fiber.New(fiber.Config{
		AppName:      "RAG Service API",
		BodyLimit:    200 * 1024 * 1024, // 200MB limit for file uploads
		ReadTimeout:  300 * time.Second, // 5 minutes timeout
		WriteTimeout: 300 * time.Second, // 5 minutes timeout
	})

	// Middleware
	app.Use(logger.New())
	app.Use(cors.New(cors.Config{
		AllowOrigins:     "*",
		AllowMethods:     "GET,POST,HEAD,PUT,DELETE,PATCH,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization,Cache-Control,X-Requested-With",
		AllowCredentials: false,
		MaxAge:           86400, // 24 hours
	}))

	// Serve static files
	app.Static("/", "./web")

	// Routes
	app.Get("/", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"message": "Hello World from RAG Service!",
			"status":  "success",
			"services": fiber.Map{
				"mysql":  "connected",
				"minio":  "connected",
				"qdrant": "connected",
				"llm":    "connected",
			},
		})
	})

	app.Get("/health", func(c *fiber.Ctx) error {
		ctx := context.Background()

		// Check MySQL
		mysqlHealth := "healthy"
		if err := mysqlAdapter.HealthCheck(); err != nil {
			mysqlHealth = "unhealthy"
		}

		// Check MinIO
		minioHealth := "healthy"
		if err := minioAdapter.HealthCheck(ctx); err != nil {
			minioHealth = "unhealthy"
		}

		// Check LLM (optional)
		llmHealth := "disabled"
		provider := strings.ToLower(cfg.LLMProvider)
		if provider == "google" {
			llmHealth = "healthy"
			if cfg.GoogleAPIKey == "" {
				llmHealth = "unhealthy"
			}
		} else if provider == "ollama" {
			llmHealth = "unhealthy"
			if oa, ok := llm.(*adapters.OllamaAdapter); ok {
				if err := oa.HealthCheck(ctx); err == nil {
					llmHealth = "healthy"
				}
			}
		}

		overallHealth := "healthy"
		if mysqlHealth != "healthy" || minioHealth != "healthy" {
			overallHealth = "unhealthy"
		}
		// Treat LLM "disabled" as acceptable
		if llmHealth != "healthy" && llmHealth != "disabled" {
			overallHealth = "unhealthy"
		}

		return c.JSON(fiber.Map{
			"status":  overallHealth,
			"service": "rag-service",
			"services": fiber.Map{
				"mysql": mysqlHealth,
				"minio": minioHealth,
				"llm":   llmHealth,
			},
		})
	})

	// Chat endpoint to test LLM
	app.Post("/chat", func(c *fiber.Ctx) error {
		var request struct {
			Message string `json:"message"`
		}

		if err := c.BodyParser(&request); err != nil {
			return c.Status(400).JSON(fiber.Map{
				"error": "Invalid request body",
			})
		}

		if request.Message == "" {
			return c.Status(400).JSON(fiber.Map{
				"error": "Message is required",
			})
		}

		ctx := context.Background()
		response, err := llm.GenerateText(ctx, request.Message)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{
				"error":   "Failed to generate response",
				"details": err.Error(),
			})
		}

		return c.JSON(fiber.Map{
			"response": response,
			"model":    modelName,
		})
	})

	// Handle CORS preflight for upload
	app.Options("/upload", func(c *fiber.Ctx) error {
		return c.SendStatus(200)
	})

	// PDF upload endpoint
	app.Post("/upload", func(c *fiber.Ctx) error {
		log.Printf("Upload request received from %s", c.IP())

		form, err := c.MultipartForm()
		if err != nil {
			log.Printf("Failed to parse multipart form: %v", err)
			return c.Status(400).JSON(fiber.Map{
				"error": "Failed to parse multipart form",
			})
		}

		files := form.File["files"]
		if len(files) == 0 {
			log.Printf("No files provided in upload request")
			return c.Status(400).JSON(fiber.Map{
				"error": "No files provided",
			})
		}

		log.Printf("Processing %d files", len(files))
		var results []map[string]interface{}
		ctx := context.Background()

		for i, file := range files {
			log.Printf("Processing file %d/%d: %s (size: %d bytes)", i+1, len(files), file.Filename, file.Size)

			// Check if file is PDF
			if !strings.HasSuffix(strings.ToLower(file.Filename), ".pdf") {
				log.Printf("File %s is not a PDF", file.Filename)
				results = append(results, map[string]interface{}{
					"filename": file.Filename,
					"status":   "error",
					"message":  "Only PDF files are supported",
				})
				continue
			}

			// Check file size (limit to 100MB per file)
			if file.Size > 100*1024*1024 {
				log.Printf("File %s is too large: %d bytes", file.Filename, file.Size)
				results = append(results, map[string]interface{}{
					"filename": file.Filename,
					"status":   "error",
					"message":  "File too large (max 100MB)",
				})
				continue
			}

			// Open file
			src, err := file.Open()
			if err != nil {
				log.Printf("Failed to open file %s: %v", file.Filename, err)
				results = append(results, map[string]interface{}{
					"filename": file.Filename,
					"status":   "error",
					"message":  "Failed to open file",
				})
				continue
			}

			// Read file data
			pdfData, err := io.ReadAll(src)
			src.Close()
			if err != nil {
				log.Printf("Failed to read file %s: %v", file.Filename, err)
				results = append(results, map[string]interface{}{
					"filename": file.Filename,
					"status":   "error",
					"message":  "Failed to read file",
				})
				continue
			}

			log.Printf("Successfully read %d bytes from %s", len(pdfData), file.Filename)

			// Process PDF
			err = ragService.ProcessPDF(ctx, file.Filename, pdfData)
			if err != nil {
				log.Printf("Failed to process PDF %s: %v", file.Filename, err)
				results = append(results, map[string]interface{}{
					"filename": file.Filename,
					"status":   "error",
					"message":  err.Error(),
				})
				continue
			}

			log.Printf("Successfully processed PDF %s", file.Filename)
			results = append(results, map[string]interface{}{
				"filename": file.Filename,
				"status":   "success",
				"message":  "PDF processed successfully",
			})
		}

		log.Printf("Upload processing completed with %d results", len(results))
		return c.JSON(fiber.Map{
			"message": "Upload processing completed",
			"results": results,
		})
	})

	// RAG query endpoint
	app.Post("/query", func(c *fiber.Ctx) error {
		var request struct {
			Question string `json:"question"`
		}

		if err := c.BodyParser(&request); err != nil {
			return c.Status(400).JSON(fiber.Map{
				"error": "Invalid request body",
			})
		}

		if request.Question == "" {
			return c.Status(400).JSON(fiber.Map{
				"error": "Question is required",
			})
		}

		ctx := context.Background()
		response, err := ragService.Query(ctx, request.Question)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{
				"error":   "Failed to process query",
				"details": err.Error(),
			})
		}

		return c.JSON(response)
	})

	// Document stats endpoint
	app.Get("/stats", func(c *fiber.Ctx) error {
		ctx := context.Background()
		stats, err := ragService.GetDocumentStats(ctx)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{
				"error":   "Failed to get document stats",
				"details": err.Error(),
			})
		}

		return c.JSON(stats)
	})

	// Handle CORS preflight for sessions
	app.Options("/sessions", func(c *fiber.Ctx) error {
		return c.SendStatus(200)
	})
	app.Options("/sessions/*", func(c *fiber.Ctx) error {
		return c.SendStatus(200)
	})

	// Chat session management endpoints
	app.Post("/sessions", func(c *fiber.Ctx) error {
		var request struct {
			Title string `json:"title"`
		}

		if err := c.BodyParser(&request); err != nil {
			return c.Status(400).JSON(fiber.Map{
				"error": "Invalid request body",
			})
		}

		if request.Title == "" {
			request.Title = "New Chat"
		}

		session, err := ragService.DatabaseSchema.CreateChatSession(request.Title)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{
				"error":   "Failed to create chat session",
				"details": err.Error(),
			})
		}

		return c.JSON(session)
	})

	app.Get("/sessions", func(c *fiber.Ctx) error {
		limit := 50
		offset := 0

		sessions, err := ragService.DatabaseSchema.GetChatSessions(limit, offset)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{
				"error":   "Failed to get chat sessions",
				"details": err.Error(),
			})
		}

		return c.JSON(sessions)
	})

	app.Get("/sessions/:id", func(c *fiber.Ctx) error {
		sessionID := c.Params("id")

		session, err := ragService.DatabaseSchema.GetChatSession(sessionID)
		if err != nil {
			return c.Status(404).JSON(fiber.Map{
				"error": "Chat session not found",
			})
		}

		// Get messages for this session
		messages, err := ragService.DatabaseSchema.GetChatMessages(sessionID, 100, 0)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{
				"error":   "Failed to get chat messages",
				"details": err.Error(),
			})
		}

		return c.JSON(fiber.Map{
			"session":  session,
			"messages": messages,
		})
	})

	app.Put("/sessions/:id", func(c *fiber.Ctx) error {
		sessionID := c.Params("id")

		var request struct {
			Title string `json:"title"`
		}

		if err := c.BodyParser(&request); err != nil {
			return c.Status(400).JSON(fiber.Map{
				"error": "Invalid request body",
			})
		}

		if request.Title == "" {
			return c.Status(400).JSON(fiber.Map{
				"error": "Title is required",
			})
		}

		err := ragService.DatabaseSchema.UpdateChatSession(sessionID, request.Title)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{
				"error":   "Failed to update chat session",
				"details": err.Error(),
			})
		}

		return c.JSON(fiber.Map{
			"message": "Chat session updated successfully",
		})
	})

	app.Delete("/sessions/:id", func(c *fiber.Ctx) error {
		sessionID := c.Params("id")

		err := ragService.DatabaseSchema.DeleteChatSession(sessionID)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{
				"error":   "Failed to delete chat session",
				"details": err.Error(),
			})
		}

		return c.JSON(fiber.Map{
			"message": "Chat session deleted successfully",
		})
	})

	// Document search endpoint - find which sources contain specific topics
	app.Post("/search-sources", func(c *fiber.Ctx) error {
		var request struct {
			Query string `json:"query"`
		}

		if err := c.BodyParser(&request); err != nil {
			return c.Status(400).JSON(fiber.Map{
				"error": "Invalid request body",
			})
		}

		if request.Query == "" {
			return c.Status(400).JSON(fiber.Map{
				"error": "Query is required",
			})
		}

		// Get all documents
		documents, err := ragService.DatabaseSchema.GetAllDocuments()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{
				"error":   "Failed to get documents",
				"details": err.Error(),
			})
		}

		// Search through all documents for the query
		var relevantSources []map[string]interface{}
		queryWords := strings.Fields(strings.ToLower(request.Query))

		for _, doc := range documents {
			if doc.Status != "completed" {
				continue
			}

			// Get chunks from this document
			chunks, err := ragService.DatabaseSchema.GetChunksByDocument(doc.ID, 100, 0)
			if err != nil {
				continue
			}

			// Check if any chunk contains the query
			var relevantChunks []string
			maxScore := 0.0

			for _, chunk := range chunks {
				score := ragService.CalculateRelevanceScore(queryWords, strings.ToLower(chunk.ChunkText))
				if score > 0.1 { // Only include chunks with some relevance
					relevantChunks = append(relevantChunks, chunk.ChunkText)
					if score > maxScore {
						maxScore = score
					}
				}
			}

			// If we found relevant chunks, add this document to results
			if len(relevantChunks) > 0 {
				// Get a snippet from the most relevant chunk
				snippet := ""
				if len(relevantChunks) > 0 {
					snippet = relevantChunks[0]
					if len(snippet) > 200 {
						snippet = snippet[:200] + "..."
					}
				}

				relevantSources = append(relevantSources, map[string]interface{}{
					"document_id":     doc.ID,
					"filename":        doc.OriginalFilename,
					"relevance_score": maxScore,
					"chunk_count":     len(relevantChunks),
					"snippet":         snippet,
					"uploaded_at":     doc.CreatedAt,
				})
			}
		}

		// Sort by relevance score (highest first)
		sort.Slice(relevantSources, func(i, j int) bool {
			return relevantSources[i]["relevance_score"].(float64) > relevantSources[j]["relevance_score"].(float64)
		})

		return c.JSON(fiber.Map{
			"query":   request.Query,
			"sources": relevantSources,
			"count":   len(relevantSources),
		})
	})

	// RAG chat endpoint with session support
	app.Post("/sessions/:id/chat", func(c *fiber.Ctx) error {
		sessionID := c.Params("id")

		var request struct {
			Message string `json:"message"`
		}

		if err := c.BodyParser(&request); err != nil {
			return c.Status(400).JSON(fiber.Map{
				"error": "Invalid request body",
			})
		}

		if request.Message == "" {
			return c.Status(400).JSON(fiber.Map{
				"error": "Message is required",
			})
		}

		// Store user message
		err := ragService.DatabaseSchema.AddChatMessage(sessionID, "user", request.Message, "", 0)
		if err != nil {
			log.Printf("Warning: failed to store user message: %v", err)
		}

		// Process RAG query
		ctx := context.Background()
		response, err := ragService.Query(ctx, request.Message)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{
				"error":   "Failed to process query",
				"details": err.Error(),
			})
		}

		// Store assistant response
		sourcesJSON := `["` + strings.Join(response.Sources, `","`) + `"]`
		err = ragService.DatabaseSchema.AddChatMessage(sessionID, "assistant", response.Answer, sourcesJSON, response.Confidence)
		if err != nil {
			log.Printf("Warning: failed to store assistant message: %v", err)
		}

		return c.JSON(response)
	})

	// Flush all data endpoint
	app.Delete("/flush", func(c *fiber.Ctx) error {
		// Clear all chat sessions and messages
		err := ragService.DatabaseSchema.FlushAllData()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{
				"error":   "Failed to flush data",
				"details": err.Error(),
			})
		}

		// Clear all files from MinIO
		err = ragService.MinIOAdapter.FlushAllFiles(context.Background())
		if err != nil {
			return c.Status(500).JSON(fiber.Map{
				"error":   "Failed to flush files from MinIO",
				"details": err.Error(),
			})
		}

		return c.JSON(fiber.Map{
			"message": "All data has been successfully cleared",
		})
	})

	// File download endpoint
	app.Get("/files/:documentId/:filename", func(c *fiber.Ctx) error {
		documentID := c.Params("documentId")
		filename := c.Params("filename")

		objectName := fmt.Sprintf("%s/%s", documentID, filename)

		// Get file from MinIO
		fileData, err := ragService.MinIOAdapter.GetObject(context.Background(), "documents", objectName)
		if err != nil {
			return c.Status(404).JSON(fiber.Map{
				"error": "File not found",
			})
		}

		c.Set("Content-Type", "application/pdf")
		c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
		return c.Send(fileData)
	})

	// Graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-c
		log.Println("Gracefully shutting down...")
		app.Shutdown()
	}()

	// Start server
	log.Printf("Starting server on port %s...", cfg.Port)
	log.Fatal(app.Listen(":" + cfg.Port))
}
