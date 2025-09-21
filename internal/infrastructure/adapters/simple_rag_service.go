package adapters

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"rag-service/internal/infrastructure/config"
)

type SimpleRAGService struct {
	OllamaAdapter  *OllamaAdapter
	MinIOAdapter   *MinIOAdapter
	MySQLAdapter   *MySQLAdapter
	PDFProcessor   *PDFProcessor
	DatabaseSchema *DatabaseSchema
	Config         *config.Config
}

type SimpleRAGResponse struct {
	Answer     string   `json:"answer"`
	Sources    []string `json:"sources"`
	Confidence float64  `json:"confidence"`
	Context    string   `json:"context"`
}

type ScoredChunk struct {
	Chunk ChunkRecord
	Score float64
}

func NewSimpleRAGService(
	ollamaAdapter *OllamaAdapter,
	minioAdapter *MinIOAdapter,
	mysqlAdapter *MySQLAdapter,
	cfg *config.Config,
) *SimpleRAGService {
	return &SimpleRAGService{
		OllamaAdapter:  ollamaAdapter,
		MinIOAdapter:   minioAdapter,
		MySQLAdapter:   mysqlAdapter,
		PDFProcessor:   NewPDFProcessor(),
		DatabaseSchema: NewDatabaseSchema(mysqlAdapter.DB),
		Config:         cfg,
	}
}

func (r *SimpleRAGService) ProcessPDF(ctx context.Context, filename string, pdfData []byte) error {
	log.Printf("Processing PDF: %s", filename)

	// Generate unique document ID
	documentID := fmt.Sprintf("doc_%d", time.Now().UnixNano())

	// Store PDF in MinIO
	bucketName := "documents"
	objectName := fmt.Sprintf("%s/%s", documentID, filename)

	err := r.MinIOAdapter.PutObject(ctx, bucketName, objectName, pdfData, "application/pdf")
	if err != nil {
		return fmt.Errorf("failed to store PDF in MinIO: %w", err)
	}

	// Create document record in MySQL
	docRecord := &DocumentRecord{
		ID:               documentID,
		Filename:         objectName,
		OriginalFilename: filename,
		FileSize:         int64(len(pdfData)),
		Status:           "processing",
		ChunkCount:       0,
		Metadata:         `{"uploaded_at": "` + time.Now().Format(time.RFC3339) + `"}`,
	}

	err = r.DatabaseSchema.InsertDocument(docRecord)
	if err != nil {
		return fmt.Errorf("failed to insert document record: %w", err)
	}

	// Extract text chunks from PDF
	chunks, err := r.PDFProcessor.ExtractTextFromPDF(pdfData, filename)
	if err != nil {
		r.DatabaseSchema.UpdateDocumentStatus(documentID, "failed")
		return fmt.Errorf("failed to extract text from PDF: %w", err)
	}

	if len(chunks) == 0 {
		r.DatabaseSchema.UpdateDocumentStatus(documentID, "failed")
		return fmt.Errorf("no text chunks extracted from PDF")
	}

	// Store chunks in MySQL
	for i, chunk := range chunks {
		chunkRecord := &ChunkRecord{
			ID:         chunk.ChunkID,
			DocumentID: documentID,
			ChunkText:  chunk.Text,
			PageNumber: chunk.Page,
			ChunkIndex: i,
			WordCount:  len(strings.Fields(chunk.Text)),
			Metadata:   `{"page": ` + fmt.Sprintf("%d", chunk.Page) + `, "chunk_index": ` + fmt.Sprintf("%d", i) + `}`,
		}

		err = r.DatabaseSchema.InsertChunk(chunkRecord)
		if err != nil {
			log.Printf("Warning: failed to insert chunk record: %v", err)
		}
	}

	// Update document status and chunk count
	err = r.DatabaseSchema.UpdateDocumentChunkCount(documentID, len(chunks))
	if err != nil {
		log.Printf("Warning: failed to update chunk count: %v", err)
	}

	err = r.DatabaseSchema.UpdateDocumentStatus(documentID, "completed")
	if err != nil {
		log.Printf("Warning: failed to update document status: %v", err)
	}

	log.Printf("Successfully processed %d chunks from PDF %s (Document ID: %s)", len(chunks), filename, documentID)
	return nil
}

func (r *SimpleRAGService) Query(ctx context.Context, question string) (*SimpleRAGResponse, error) {
	log.Printf("Processing RAG query: %s", question)

	// Check if we have any documents
	documents, err := r.DatabaseSchema.GetDocuments(50, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to get documents: %w", err)
	}

	if len(documents) == 0 {
		response := &SimpleRAGResponse{
			Answer:     "I don't have any documents in my knowledge base yet. Please upload some PDF files first.",
			Sources:    []string{},
			Confidence: 0.0,
			Context:    "",
		}

		// Store query in database
		r.storeQuery(ctx, question, response)
		return response, nil
	}

	// Simple approach: Search all documents without bias
	questionWords := strings.Fields(strings.ToLower(question))

	// Get chunks from all completed documents
	var allChunks []ChunkRecord
	for _, doc := range documents {
		if doc.Status == "completed" {
			chunks, err := r.DatabaseSchema.GetChunksByDocument(doc.ID, 50, 0)
			if err != nil {
				log.Printf("Warning: failed to get chunks for document %s: %v", doc.ID, err)
				continue
			}
			allChunks = append(allChunks, chunks...)
		}
	}

	if len(allChunks) == 0 {
		response := &SimpleRAGResponse{
			Answer:     "I don't have any processed content in my knowledge base yet. Please upload some PDF files first.",
			Sources:    []string{},
			Confidence: 0.0,
			Context:    "",
		}

		// Store query in database
		r.storeQuery(ctx, question, response)
		return response, nil
	}

	// Score all chunks based purely on text similarity
	scoredChunks := make([]ScoredChunk, len(allChunks))
	for i, chunk := range allChunks {
		score := r.CalculateRelevanceScore(questionWords, strings.ToLower(chunk.ChunkText))
		scoredChunks[i] = ScoredChunk{
			Chunk: chunk,
			Score: score,
		}
	}

	// Debug: Log top 5 chunks with their scores
	log.Printf("Question: %s", question)
	for i, scoredChunk := range scoredChunks {
		if i < 5 {
			log.Printf("Chunk %d score: %.2f, text preview: %.100s...", i, scoredChunk.Score, scoredChunk.Chunk.ChunkText)
		}
	}

	// Sort by relevance score (highest first)
	sort.Slice(scoredChunks, func(i, j int) bool {
		return scoredChunks[i].Score > scoredChunks[j].Score
	})

	// Take top 3 most relevant chunks
	topChunks := scoredChunks
	if len(scoredChunks) > 3 {
		topChunks = scoredChunks[:3]
	}

	// Build context from most relevant chunks
	var contextParts []string
	bestScore := 0.0

	for _, scoredChunk := range topChunks {
		if scoredChunk.Score > 0.1 { // Only include chunks with some relevance
			contextParts = append(contextParts, scoredChunk.Chunk.ChunkText)

			// Track the best score
			if scoredChunk.Score > bestScore {
				bestScore = scoredChunk.Score
			}
		}
	}

	if len(contextParts) == 0 {
		response := &SimpleRAGResponse{
			Answer:     "I don't have enough relevant information to answer that question accurately.",
			Sources:    []string{},
			Confidence: 0.0,
			Context:    "",
		}

		// Store query in database
		r.storeQuery(ctx, question, response)
		return response, nil
	}

	context := strings.Join(contextParts, "\n\n")

	// Generate answer using Ollama with context
	prompt := fmt.Sprintf(`Answer this question using ONLY the information provided in the context below. Give a direct, specific answer.

CONTEXT:
%s

QUESTION: %s

ANSWER:`, context, question)

	answer, err := r.OllamaAdapter.GenerateText(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to generate answer: %w", err)
	}

	// Check if the answer indicates lack of knowledge
	answerLower := strings.ToLower(answer)
	if strings.Contains(answerLower, "i don't have that information") ||
		strings.Contains(answerLower, "i don't have enough information") ||
		strings.Contains(answerLower, "not found in the provided documents") ||
		strings.Contains(answerLower, "not available in the context") {
		response := &SimpleRAGResponse{
			Answer:     "I don't have that information in the provided documents.",
			Sources:    []string{},
			Confidence: 0.0,
			Context:    context,
		}

		// Store query in database
		r.storeQuery(ctx, question, response)
		return response, nil
	}

	// Include multiple relevant sources with document ID for download
	var sources []string
	topSources := r.getTopRelevantSources(questionWords, documents, 5)
	for _, source := range topSources {
		formattedSource := r.formatSourceWithDocumentID(source.Filename, documents)
		sources = append(sources, formattedSource)
	}

	// Calculate confidence based on best score
	confidence := bestScore
	if confidence > 1.0 {
		confidence = 1.0
	}

	response := &SimpleRAGResponse{
		Answer:     answer,
		Sources:    sources,
		Confidence: confidence,
		Context:    context,
	}

	// Store query in database
	r.storeQuery(ctx, question, response)
	return response, nil
}

func (r *SimpleRAGService) storeQuery(ctx context.Context, question string, response *SimpleRAGResponse) {
	queryID := fmt.Sprintf("query_%d", time.Now().UnixNano())

	// Convert sources to JSON string
	sourcesJSON := `["` + strings.Join(response.Sources, `","`) + `"]`

	queryRecord := &QueryRecord{
		ID:         queryID,
		Question:   question,
		Answer:     response.Answer,
		Confidence: response.Confidence,
		Sources:    sourcesJSON,
		Context:    response.Context,
	}

	err := r.DatabaseSchema.InsertQuery(queryRecord)
	if err != nil {
		log.Printf("Warning: failed to store query: %v", err)
	}
}

func (r *SimpleRAGService) GetDocumentStats(ctx context.Context) (map[string]interface{}, error) {
	documents, err := r.DatabaseSchema.GetDocuments(100, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to get documents: %w", err)
	}

	completedCount := 0
	totalChunks := 0
	for _, doc := range documents {
		if doc.Status == "completed" {
			completedCount++
			totalChunks += doc.ChunkCount
		}
	}

	return map[string]interface{}{
		"total_documents":     len(documents),
		"completed_documents": completedCount,
		"total_chunks":        totalChunks,
	}, nil
}

// CalculateRelevanceScore calculates a sophisticated relevance score
func (r *SimpleRAGService) CalculateRelevanceScore(questionWords []string, chunkText string) float64 {
	score := 0.0
	chunkWords := strings.Fields(strings.ToLower(chunkText))
	questionLower := strings.ToLower(strings.Join(questionWords, " "))
	chunkLower := strings.ToLower(chunkText)

	// 1. Exact phrase matching (highest priority)
	if strings.Contains(chunkLower, questionLower) {
		score += 100.0
	}

	// 2. Exact word matches (case insensitive)
	exactMatches := 0
	for _, qWord := range questionWords {
		qWordLower := strings.ToLower(qWord)
		for _, cWord := range chunkWords {
			if cWord == qWordLower {
				exactMatches++
				score += 20.0
			}
		}
	}

	// 3. Partial word matches
	for _, qWord := range questionWords {
		qWordLower := strings.ToLower(qWord)
		if len(qWordLower) > 2 {
			for _, cWord := range chunkWords {
				if strings.Contains(cWord, qWordLower) || strings.Contains(qWordLower, cWord) {
					score += 5.0
				}
			}
		}
	}

	// 4. Handle encoding issues - try with common character substitutions
	normalizedQuestion := strings.ToLower(strings.Join(questionWords, " "))
	normalizedChunk := strings.ToLower(chunkText)

	// Common character encoding issues
	replacements := map[string]string{
		"ó": "o", "á": "a", "é": "e", "í": "i", "ú": "u",
		"ñ": "n", "ç": "c", "ü": "u", "ö": "o", "ä": "a",
		"": "o", // Common replacement for ó
	}

	for old, new := range replacements {
		normalizedQuestion = strings.ReplaceAll(normalizedQuestion, old, new)
		normalizedChunk = strings.ReplaceAll(normalizedChunk, old, new)
	}

	// Check for matches in normalized text
	if strings.Contains(normalizedChunk, normalizedQuestion) {
		score += 50.0
	}

	// Check for word matches in normalized text
	normalizedQuestionWords := strings.Fields(normalizedQuestion)
	for _, qWord := range normalizedQuestionWords {
		if len(qWord) > 2 && strings.Contains(normalizedChunk, qWord) {
			score += 10.0
		}
	}

	// 4. No biased keywords - pure text matching only

	// 5. Normalize by question length to avoid bias
	if len(questionWords) > 0 {
		score = score / float64(len(questionWords))
	}

	// 6. No length bias - treat all chunks equally

	return score
}

// Removed document relevance function - no longer using document-level filtering

// formatSourceWithDocumentID formats a source with document ID for download
func (r *SimpleRAGService) formatSourceWithDocumentID(source string, documents []DocumentRecord) string {
	if source == "" {
		return ""
	}

	// Find the document ID for the source
	for _, doc := range documents {
		if doc.OriginalFilename == source {
			return doc.ID + "|" + source
		}
	}

	// Fallback: return source as-is
	return source
}

// SourceScore represents a document with its relevance score
type SourceScore struct {
	Filename string
	Score    float64
}

// getTopRelevantSources finds the top N most relevant sources for a query
func (r *SimpleRAGService) getTopRelevantSources(questionWords []string, documents []DocumentRecord, limit int) []SourceScore {
	var sourceScores []SourceScore

	for _, doc := range documents {
		if doc.Status != "completed" {
			continue
		}

		// Get chunks from this document
		chunks, err := r.DatabaseSchema.GetChunksByDocument(doc.ID, 50, 0)
		if err != nil {
			continue
		}

		// Calculate relevance score for this document
		maxScore := 0.0
		for _, chunk := range chunks {
			score := r.CalculateRelevanceScore(questionWords, strings.ToLower(chunk.ChunkText))
			if score > maxScore {
				maxScore = score
			}
		}

		// Only include documents with some relevance
		if maxScore > 0.1 {
			sourceScores = append(sourceScores, SourceScore{
				Filename: doc.OriginalFilename,
				Score:    maxScore,
			})
		}
	}

	// Sort by score (highest first)
	sort.Slice(sourceScores, func(i, j int) bool {
		return sourceScores[i].Score > sourceScores[j].Score
	})

	// Return top N sources
	if len(sourceScores) > limit {
		return sourceScores[:limit]
	}
	return sourceScores
}

// searchAllDocuments is the fallback method when document-level filtering fails
func (r *SimpleRAGService) searchAllDocuments(ctx context.Context, question string, documents []DocumentRecord) (*SimpleRAGResponse, error) {
	// Get chunks from all completed documents
	var allChunks []ChunkRecord
	for _, doc := range documents {
		if doc.Status == "completed" {
			chunks, err := r.DatabaseSchema.GetChunksByDocument(doc.ID, 50, 0)
			if err != nil {
				log.Printf("Warning: failed to get chunks for document %s: %v", doc.ID, err)
				continue
			}
			allChunks = append(allChunks, chunks...)
		}
	}

	if len(allChunks) == 0 {
		response := &SimpleRAGResponse{
			Answer:     "I don't have any processed content in my knowledge base yet. Please upload some PDF files first.",
			Sources:    []string{},
			Confidence: 0.0,
			Context:    "",
		}

		// Store query in database
		r.storeQuery(ctx, question, response)
		return response, nil
	}

	// Score all chunks
	questionWords := strings.Fields(strings.ToLower(question))
	scoredChunks := make([]ScoredChunk, len(allChunks))

	for i, chunk := range allChunks {
		score := r.CalculateRelevanceScore(questionWords, strings.ToLower(chunk.ChunkText))
		scoredChunks[i] = ScoredChunk{
			Chunk: chunk,
			Score: score,
		}
	}

	// Sort by relevance score (highest first)
	sort.Slice(scoredChunks, func(i, j int) bool {
		return scoredChunks[i].Score > scoredChunks[j].Score
	})

	// Take top 3 most relevant chunks
	topChunks := scoredChunks
	if len(scoredChunks) > 3 {
		topChunks = scoredChunks[:3]
	}

	// Build context from most relevant chunks
	var contextParts []string
	bestScore := 0.0

	for _, scoredChunk := range topChunks {
		if scoredChunk.Score > 0.1 { // Only include chunks with some relevance
			contextParts = append(contextParts, scoredChunk.Chunk.ChunkText)

			// Track the best score
			if scoredChunk.Score > bestScore {
				bestScore = scoredChunk.Score
			}
		}
	}

	if len(contextParts) == 0 {
		response := &SimpleRAGResponse{
			Answer:     "I don't have enough relevant information to answer that question accurately.",
			Sources:    []string{},
			Confidence: 0.0,
			Context:    "",
		}

		// Store query in database
		r.storeQuery(ctx, question, response)
		return response, nil
	}

	context := strings.Join(contextParts, "\n\n")

	// Generate answer using Ollama with context
	prompt := fmt.Sprintf(`Answer this question using ONLY the information provided in the context below. Give a direct, specific answer.

CONTEXT:
%s

QUESTION: %s

ANSWER:`, context, question)

	answer, err := r.OllamaAdapter.GenerateText(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to generate answer: %w", err)
	}

	// Check if the answer indicates lack of knowledge
	answerLower := strings.ToLower(answer)
	if strings.Contains(answerLower, "i don't have that information") ||
		strings.Contains(answerLower, "i don't have enough information") ||
		strings.Contains(answerLower, "not found in the provided documents") ||
		strings.Contains(answerLower, "not available in the context") {
		response := &SimpleRAGResponse{
			Answer:     "I don't have that information in the provided documents.",
			Sources:    []string{},
			Confidence: 0.0,
			Context:    context,
		}

		// Store query in database
		r.storeQuery(ctx, question, response)
		return response, nil
	}

	// Include multiple relevant sources with document ID for download
	var sources []string
	topSources := r.getTopRelevantSources(questionWords, documents, 5)
	for _, source := range topSources {
		formattedSource := r.formatSourceWithDocumentID(source.Filename, documents)
		sources = append(sources, formattedSource)
	}

	// Calculate confidence based on best score
	confidence := bestScore
	if confidence > 1.0 {
		confidence = 1.0
	}

	response := &SimpleRAGResponse{
		Answer:     answer,
		Sources:    sources,
		Confidence: confidence,
		Context:    context,
	}

	// Store query in database
	r.storeQuery(ctx, question, response)
	return response, nil
}
