package adapters

import (
	"database/sql"
	"fmt"
	"log"
	"time"
)

type DatabaseSchema struct {
	DB *sql.DB
}

func NewDatabaseSchema(db *sql.DB) *DatabaseSchema {
	return &DatabaseSchema{DB: db}
}

func (ds *DatabaseSchema) CreateTables() error {
	// Create documents table
	createDocumentsTable := `
	CREATE TABLE IF NOT EXISTS documents (
		id VARCHAR(255) PRIMARY KEY,
		filename VARCHAR(255) NOT NULL,
		original_filename VARCHAR(255) NOT NULL,
		file_size BIGINT NOT NULL,
		upload_date TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		status ENUM('processing', 'completed', 'failed') DEFAULT 'processing',
		chunk_count INT DEFAULT 0,
		metadata JSON,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
	)`

	// Create document_chunks table
	createChunksTable := `
	CREATE TABLE IF NOT EXISTS document_chunks (
		id VARCHAR(255) PRIMARY KEY,
		document_id VARCHAR(255) NOT NULL,
		chunk_text TEXT NOT NULL,
		page_number INT NOT NULL,
		chunk_index INT NOT NULL,
		word_count INT NOT NULL,
		metadata JSON,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (document_id) REFERENCES documents(id) ON DELETE CASCADE
	)`

	// Create document_queries table for tracking queries
	createQueriesTable := `
	CREATE TABLE IF NOT EXISTS document_queries (
		id VARCHAR(255) PRIMARY KEY,
		question TEXT NOT NULL,
		answer TEXT NOT NULL,
		confidence FLOAT NOT NULL,
		sources JSON,
		context TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`

	// Create chat_sessions table
	createChatSessionsTable := `
	CREATE TABLE IF NOT EXISTS chat_sessions (
		id VARCHAR(255) PRIMARY KEY,
		title VARCHAR(255) NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
	)`

	// Create chat_messages table
	createChatMessagesTable := `
	CREATE TABLE IF NOT EXISTS chat_messages (
		id VARCHAR(255) PRIMARY KEY,
		session_id VARCHAR(255) NOT NULL,
		role ENUM('user', 'assistant') NOT NULL,
		content TEXT NOT NULL,
		sources JSON,
		confidence FLOAT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (session_id) REFERENCES chat_sessions(id) ON DELETE CASCADE
	)`

	tables := []string{
		createDocumentsTable,
		createChunksTable,
		createQueriesTable,
		createChatSessionsTable,
		createChatMessagesTable,
	}

	for _, table := range tables {
		if _, err := ds.DB.Exec(table); err != nil {
			return fmt.Errorf("failed to create table: %w", err)
		}
	}

	log.Println("✅ Database tables created successfully")
	return nil
}

// GetAllDocuments retrieves all documents from the database
func (ds *DatabaseSchema) GetAllDocuments() ([]DocumentRecord, error) {
	query := `SELECT id, original_filename, status, created_at, updated_at FROM documents ORDER BY created_at DESC`

	rows, err := ds.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var documents []DocumentRecord
	for rows.Next() {
		var doc DocumentRecord
		err := rows.Scan(&doc.ID, &doc.OriginalFilename, &doc.Status, &doc.CreatedAt, &doc.UpdatedAt)
		if err != nil {
			return nil, err
		}
		documents = append(documents, doc)
	}

	return documents, nil
}

// FlushAllData clears all data from the database
func (ds *DatabaseSchema) FlushAllData() error {
	// Delete all chat messages first (due to foreign key constraints)
	_, err := ds.DB.Exec("DELETE FROM chat_messages")
	if err != nil {
		return fmt.Errorf("failed to delete chat messages: %w", err)
	}

	// Delete all chat sessions
	_, err = ds.DB.Exec("DELETE FROM chat_sessions")
	if err != nil {
		return fmt.Errorf("failed to delete chat sessions: %w", err)
	}

	// Delete all document chunks
	_, err = ds.DB.Exec("DELETE FROM document_chunks")
	if err != nil {
		return fmt.Errorf("failed to delete document chunks: %w", err)
	}

	// Delete all documents
	_, err = ds.DB.Exec("DELETE FROM documents")
	if err != nil {
		return fmt.Errorf("failed to delete documents: %w", err)
	}

	log.Println("✅ All data flushed successfully")
	return nil
}

func (ds *DatabaseSchema) InsertDocument(doc *DocumentRecord) error {
	query := `
	INSERT INTO documents (id, filename, original_filename, file_size, status, chunk_count, metadata)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	ON DUPLICATE KEY UPDATE
		status = VALUES(status),
		chunk_count = VALUES(chunk_count),
		metadata = VALUES(metadata),
		updated_at = CURRENT_TIMESTAMP`

	_, err := ds.DB.Exec(query, doc.ID, doc.Filename, doc.OriginalFilename, doc.FileSize, doc.Status, doc.ChunkCount, doc.Metadata)
	return err
}

func (ds *DatabaseSchema) InsertChunk(chunk *ChunkRecord) error {
	query := `
	INSERT INTO document_chunks (id, document_id, chunk_text, page_number, chunk_index, word_count, metadata)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	ON DUPLICATE KEY UPDATE
		chunk_text = VALUES(chunk_text),
		metadata = VALUES(metadata)`

	_, err := ds.DB.Exec(query, chunk.ID, chunk.DocumentID, chunk.ChunkText, chunk.PageNumber, chunk.ChunkIndex, chunk.WordCount, chunk.Metadata)
	return err
}

func (ds *DatabaseSchema) InsertQuery(query *QueryRecord) error {
	sqlQuery := `
	INSERT INTO document_queries (id, question, answer, confidence, sources, context)
	VALUES (?, ?, ?, ?, ?, ?)`

	_, err := ds.DB.Exec(sqlQuery, query.ID, query.Question, query.Answer, query.Confidence, query.Sources, query.Context)
	return err
}

func (ds *DatabaseSchema) GetDocument(id string) (*DocumentRecord, error) {
	query := `SELECT id, filename, original_filename, file_size, status, chunk_count, metadata, created_at, updated_at FROM documents WHERE id = ?`

	var doc DocumentRecord
	err := ds.DB.QueryRow(query, id).Scan(
		&doc.ID, &doc.Filename, &doc.OriginalFilename, &doc.FileSize, &doc.Status,
		&doc.ChunkCount, &doc.Metadata, &doc.CreatedAt, &doc.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	return &doc, nil
}

func (ds *DatabaseSchema) GetDocuments(limit, offset int) ([]DocumentRecord, error) {
	query := `SELECT id, filename, original_filename, file_size, status, chunk_count, metadata, created_at, updated_at 
			  FROM documents ORDER BY created_at DESC LIMIT ? OFFSET ?`

	rows, err := ds.DB.Query(query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var documents []DocumentRecord
	for rows.Next() {
		var doc DocumentRecord
		err := rows.Scan(
			&doc.ID, &doc.Filename, &doc.OriginalFilename, &doc.FileSize, &doc.Status,
			&doc.ChunkCount, &doc.Metadata, &doc.CreatedAt, &doc.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		documents = append(documents, doc)
	}

	return documents, nil
}

func (ds *DatabaseSchema) UpdateDocumentStatus(id, status string) error {
	query := `UPDATE documents SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := ds.DB.Exec(query, status, id)
	return err
}

func (ds *DatabaseSchema) UpdateDocumentChunkCount(id string, count int) error {
	query := `UPDATE documents SET chunk_count = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := ds.DB.Exec(query, count, id)
	return err
}

func (ds *DatabaseSchema) GetQueries(limit, offset int) ([]QueryRecord, error) {
	query := `SELECT id, question, answer, confidence, sources, context, created_at 
			  FROM document_queries ORDER BY created_at DESC LIMIT ? OFFSET ?`

	rows, err := ds.DB.Query(query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var queries []QueryRecord
	for rows.Next() {
		var q QueryRecord
		err := rows.Scan(
			&q.ID, &q.Question, &q.Answer, &q.Confidence, &q.Sources, &q.Context, &q.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		queries = append(queries, q)
	}

	return queries, nil
}

// Chat session management methods
func (ds *DatabaseSchema) CreateChatSession(title string) (*ChatSession, error) {
	sessionID := fmt.Sprintf("session_%d", time.Now().UnixNano())

	session := &ChatSession{
		ID:        sessionID,
		Title:     title,
		CreatedAt: time.Now().Format(time.RFC3339),
		UpdatedAt: time.Now().Format(time.RFC3339),
	}

	query := `INSERT INTO chat_sessions (id, title) VALUES (?, ?)`
	_, err := ds.DB.Exec(query, session.ID, session.Title)
	if err != nil {
		return nil, err
	}

	return session, nil
}

func (ds *DatabaseSchema) GetChatSessions(limit, offset int) ([]ChatSession, error) {
	query := `SELECT id, title, created_at, updated_at FROM chat_sessions ORDER BY updated_at DESC LIMIT ? OFFSET ?`

	rows, err := ds.DB.Query(query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []ChatSession
	for rows.Next() {
		var session ChatSession
		err := rows.Scan(&session.ID, &session.Title, &session.CreatedAt, &session.UpdatedAt)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}

	return sessions, nil
}

func (ds *DatabaseSchema) GetChatSession(sessionID string) (*ChatSession, error) {
	query := `SELECT id, title, created_at, updated_at FROM chat_sessions WHERE id = ?`

	var session ChatSession
	err := ds.DB.QueryRow(query, sessionID).Scan(&session.ID, &session.Title, &session.CreatedAt, &session.UpdatedAt)
	if err != nil {
		return nil, err
	}

	return &session, nil
}

func (ds *DatabaseSchema) UpdateChatSession(sessionID, title string) error {
	query := `UPDATE chat_sessions SET title = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := ds.DB.Exec(query, title, sessionID)
	return err
}

func (ds *DatabaseSchema) DeleteChatSession(sessionID string) error {
	query := `DELETE FROM chat_sessions WHERE id = ?`
	_, err := ds.DB.Exec(query, sessionID)
	return err
}

func (ds *DatabaseSchema) AddChatMessage(sessionID, role, content, sources string, confidence float64) error {
	messageID := fmt.Sprintf("msg_%d", time.Now().UnixNano())

	query := `INSERT INTO chat_messages (id, session_id, role, content, sources, confidence) VALUES (?, ?, ?, ?, ?, ?)`
	_, err := ds.DB.Exec(query, messageID, sessionID, role, content, sources, confidence)
	return err
}

func (ds *DatabaseSchema) GetChatMessages(sessionID string, limit, offset int) ([]ChatMessage, error) {
	query := `SELECT id, session_id, role, content, sources, confidence, created_at 
			  FROM chat_messages WHERE session_id = ? ORDER BY created_at ASC LIMIT ? OFFSET ?`

	rows, err := ds.DB.Query(query, sessionID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []ChatMessage
	for rows.Next() {
		var msg ChatMessage
		err := rows.Scan(&msg.ID, &msg.SessionID, &msg.Role, &msg.Content, &msg.Sources, &msg.Confidence, &msg.CreatedAt)
		if err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}

	return messages, nil
}

func (ds *DatabaseSchema) GetChunksByDocument(documentID string, limit, offset int) ([]ChunkRecord, error) {
	query := `SELECT id, document_id, chunk_text, page_number, chunk_index, word_count, metadata, created_at 
			  FROM document_chunks WHERE document_id = ? ORDER BY chunk_index ASC LIMIT ? OFFSET ?`

	rows, err := ds.DB.Query(query, documentID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chunks []ChunkRecord
	for rows.Next() {
		var chunk ChunkRecord
		err := rows.Scan(&chunk.ID, &chunk.DocumentID, &chunk.ChunkText, &chunk.PageNumber, &chunk.ChunkIndex, &chunk.WordCount, &chunk.Metadata, &chunk.CreatedAt)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

// Document and Chunk record structures
type DocumentRecord struct {
	ID               string `json:"id"`
	Filename         string `json:"filename"`
	OriginalFilename string `json:"original_filename"`
	FileSize         int64  `json:"file_size"`
	Status           string `json:"status"`
	ChunkCount       int    `json:"chunk_count"`
	Metadata         string `json:"metadata"` // JSON string
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

type ChunkRecord struct {
	ID         string `json:"id"`
	DocumentID string `json:"document_id"`
	ChunkText  string `json:"chunk_text"`
	PageNumber int    `json:"page_number"`
	ChunkIndex int    `json:"chunk_index"`
	WordCount  int    `json:"word_count"`
	Metadata   string `json:"metadata"` // JSON string
	CreatedAt  string `json:"created_at"`
}

type QueryRecord struct {
	ID         string  `json:"id"`
	Question   string  `json:"question"`
	Answer     string  `json:"answer"`
	Confidence float64 `json:"confidence"`
	Sources    string  `json:"sources"` // JSON string
	Context    string  `json:"context"`
	CreatedAt  string  `json:"created_at"`
}

type ChatSession struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type ChatMessage struct {
	ID         string  `json:"id"`
	SessionID  string  `json:"session_id"`
	Role       string  `json:"role"`
	Content    string  `json:"content"`
	Sources    string  `json:"sources"` // JSON string
	Confidence float64 `json:"confidence"`
	CreatedAt  string  `json:"created_at"`
}
