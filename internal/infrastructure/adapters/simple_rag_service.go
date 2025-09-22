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
	LLM            LLMClient
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
	llm LLMClient,
	minioAdapter *MinIOAdapter,
	mysqlAdapter *MySQLAdapter,
	cfg *config.Config,
) *SimpleRAGService {
	return &SimpleRAGService{
		LLM:            llm,
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

	// Take top 5 most relevant chunks
	topChunks := scoredChunks
	if len(scoredChunks) > 5 {
		topChunks = scoredChunks[:5]
	}

	// Build context from most relevant chunks
	var contextParts []string
	bestScore := 0.0

	for _, scoredChunk := range topChunks {
		if scoredChunk.Score > 0.2 { // Only include chunks with some relevance
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

	// Generate answer using LLM with context
	var prompt string
	if r.Config != nil && r.Config.AppLanguage == "fa" {
		prompt = fmt.Sprintf(`فقط با استفاده از اطلاعات «متن زمینه» زیر پاسخ بده. پاسخ باید دقیق، واضح و به زبان فارسی باشد. اگر پاسخ در متن نبود، فقط بگو: «اطلاعات کافی در متن موجود نیست».

متن زمینه:
%s

پرسش: %s

پاسخ:`, context, question)
	} else {
		prompt = fmt.Sprintf(`Answer this question using ONLY the information provided in the context below. Give a direct, specific answer.

CONTEXT:
%s

QUESTION: %s

ANSWER:`, context, question)
	}

	answer, err := r.LLM.GenerateText(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to generate answer: %w", err)
	}

	// Check if the answer indicates lack of knowledge (EN + FA)
	answerLower := strings.ToLower(answer)
	missingFa := strings.Contains(answer, "اطلاعات کافی در متن موجود نیست")
	if strings.Contains(answerLower, "i don't have that information") ||
		strings.Contains(answerLower, "i don't have enough information") ||
		strings.Contains(answerLower, "not found in the provided documents") ||
		strings.Contains(answerLower, "not available in the context") ||
		missingFa {
		msg := "I don't have that information in the provided documents."
		if r.Config != nil && r.Config.AppLanguage == "fa" {
			msg = "این اطلاعات در اسناد موجود نیست."
		}
		response := &SimpleRAGResponse{
			Answer:     msg,
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

// CalculateRelevanceScore calculates a relevance score using token matches,
// simple term-frequency weighting, and query coverage. This is a lightweight
// alternative to embeddings to improve ranking quality.
func (r *SimpleRAGService) CalculateRelevanceScore(questionWords []string, chunkText string) float64 {
	score := 0.0

	// Normalize and tokenize
	normalize := func(s string) string {
		s = strings.ToLower(s)
		// Basic accent folding
		replacements := map[string]string{
			"ó": "o", "á": "a", "é": "e", "í": "i", "ú": "u",
			"ñ": "n", "ç": "c", "ü": "u", "ö": "o", "ä": "a",
		}
		for old, new := range replacements {
			s = strings.ReplaceAll(s, old, new)
		}
		// Replace non-alphanumerics with space
		var b strings.Builder
		for _, r := range s {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' {
				b.WriteRune(r)
			} else {
				b.WriteRune(' ')
			}
		}
		return strings.Join(strings.Fields(b.String()), " ")
	}

	normalizedChunk := normalize(chunkText)
	normalizedQuestion := normalize(strings.Join(questionWords, " "))

	chunkTokens := strings.Fields(normalizedChunk)
	questionTokens := strings.Fields(normalizedQuestion)
	if len(chunkTokens) == 0 || len(questionTokens) == 0 {
		return 0.0
	}

	// Exact phrase bonus
	if strings.Contains(normalizedChunk, normalizedQuestion) && len(normalizedQuestion) >= 8 {
		score += 40.0
	}

	// Build term frequency for chunk
	chunkTF := make(map[string]int)
	for _, t := range chunkTokens {
		chunkTF[t]++
	}

	// Match scoring with TF weighting and partials
	covered := 0
	for _, q := range questionTokens {
		tf := chunkTF[q]
		if tf > 0 {
			covered++
			// Heavier weight for exact matches
			score += 12.0 * (1.0 + 0.1*float64(tf-1))
			continue
		}
		// Partial match if no exact; only for tokens length >= 4
		if len(q) >= 4 {
			partialHit := false
			for token := range chunkTF {
				if strings.Contains(token, q) || strings.Contains(q, token) {
					partialHit = true
					break
				}
			}
			if partialHit {
				score += 4.0
			}
		}
	}

	// Coverage reward: proportion of query terms matched
	coverage := float64(covered) / float64(len(questionTokens))
	score += 20.0 * coverage

	// Normalize by query length to reduce bias
	score = score / (1.0 + 0.05*float64(len(questionTokens)))

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

	// Take top N most relevant chunks
	topChunks := scoredChunks
	if len(scoredChunks) > 8 {
		topChunks = scoredChunks[:8]
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

	// If no context and app language is Persian, attempt cross-lingual fallback: translate question to English and retry retrieval
	if len(contextParts) == 0 && r.Config != nil && r.Config.AppLanguage == "fa" {
		translated, tErr := r.translateToEnglish(ctx, question)
		if tErr == nil && strings.TrimSpace(translated) != "" {
			enWords := strings.Fields(strings.ToLower(translated))
			// Rescore
			rescored := make([]ScoredChunk, len(allChunks))
			for i, chunk := range allChunks {
				score := r.CalculateRelevanceScore(enWords, strings.ToLower(chunk.ChunkText))
				rescored[i] = ScoredChunk{Chunk: chunk, Score: score}
			}
			sort.Slice(rescored, func(i, j int) bool { return rescored[i].Score > rescored[j].Score })
			topChunks = rescored
			if len(rescored) > 3 {
				topChunks = rescored[:3]
			}
			contextParts = contextParts[:0]
			bestScore = 0.0
			for _, scoredChunk := range topChunks {
				if scoredChunk.Score > 0.1 {
					contextParts = append(contextParts, scoredChunk.Chunk.ChunkText)
					if scoredChunk.Score > bestScore {
						bestScore = scoredChunk.Score
					}
				}
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

	// Build final context and cap its length to avoid exceeding model limits
	context := strings.Join(contextParts, "\n\n---\n\n")
	if len(context) > 12000 {
		context = context[:12000]
	}

	// Generate answer using LLM with context
	prompt := fmt.Sprintf(`Answer this question using ONLY the information provided in the context below. Give a direct, specific answer.

CONTEXT:
%s

QUESTION: %s

ANSWER:`, context, question)

	answer, err := r.LLM.GenerateText(ctx, prompt)
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

// translateToEnglish uses the LLM to translate input text to English, returning plain text only
func (r *SimpleRAGService) translateToEnglish(ctx context.Context, text string) (string, error) {
	prompt := "Translate the following text to English. Return only the translation without quotes or extra commentary.\n\nText:\n" + text
	return r.LLM.GenerateText(ctx, prompt)
}
