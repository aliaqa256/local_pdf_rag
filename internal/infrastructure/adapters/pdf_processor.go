package adapters

import (
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"
	"unicode"

	"github.com/ledongthuc/pdf"
)

type PDFProcessor struct{}

type PDFChunk struct {
	Text     string
	Page     int
	ChunkID  string
	Document string
	Metadata map[string]interface{}
}

func NewPDFProcessor() *PDFProcessor {
	return &PDFProcessor{}
}

func (p *PDFProcessor) ExtractTextFromPDF(pdfData []byte, filename string) ([]PDFChunk, error) {
	log.Printf("Processing PDF %s", filename)
	
	// Create a reader from the PDF data
	reader := strings.NewReader(string(pdfData))
	
	// Open PDF
	pdfReader, err := pdf.NewReader(reader, int64(len(pdfData)))
	if err != nil {
		return nil, fmt.Errorf("failed to open PDF: %w", err)
	}
	
	var allText []string
	var chunks []PDFChunk
	chunkID := 0
	
	// Extract text from each page
	for pageNum := 1; pageNum <= pdfReader.NumPage(); pageNum++ {
		page := pdfReader.Page(pageNum)
		if page.V.IsNull() {
			continue
		}
		
		// Extract text from page
		content, err := page.GetPlainText(nil)
		if err != nil {
			log.Printf("Warning: failed to extract text from page %d: %v", pageNum, err)
			continue
		}
		
		// Clean and normalize text
		cleanedText := p.cleanText(content)
		if cleanedText == "" {
			continue
		}
		
		allText = append(allText, cleanedText)
		
		// Split page content into chunks
		pageChunks := p.splitIntoChunks(cleanedText, pageNum, filename)
		for i := range pageChunks {
			pageChunks[i].ChunkID = fmt.Sprintf("%s_p%d_c%d", filename, pageNum, chunkID)
			chunkID++
		}
		chunks = append(chunks, pageChunks...)
	}
	
	log.Printf("Extracted %d chunks from PDF %s (%d pages)", len(chunks), filename, len(allText))
	return chunks, nil
}

func (p *PDFProcessor) cleanText(text string) string {
	// Remove excessive whitespace
	text = regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")
	
	// Remove page numbers and headers/footers (simple patterns)
	text = regexp.MustCompile(`^\s*\d+\s*$`).ReplaceAllString(text, "")
	text = regexp.MustCompile(`\n\s*\d+\s*\n`).ReplaceAllString(text, "\n")
	
	// Remove excessive newlines
	text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")
	
	// Remove non-printable characters except newlines and tabs
	var result strings.Builder
	for _, r := range text {
		if unicode.IsPrint(r) || r == '\n' || r == '\t' {
			result.WriteRune(r)
		}
	}

	return strings.TrimSpace(result.String())
}

func (p *PDFProcessor) splitIntoChunks(text string, pageNum int, filename string) []PDFChunk {
	const maxChunkSize = 1000 // characters
	const overlapSize = 200   // characters for overlap between chunks

	var chunks []PDFChunk
	words := strings.Fields(text)

	if len(words) == 0 {
		return chunks
	}

	var currentChunk strings.Builder
	chunkID := 1

	for i, word := range words {
		currentChunk.WriteString(word)
		currentChunk.WriteString(" ")

		// Check if we should create a chunk
		if currentChunk.Len() >= maxChunkSize || i == len(words)-1 {
			chunkText := strings.TrimSpace(currentChunk.String())
			if len(chunkText) > 50 { // Only create chunks with meaningful content
				chunk := PDFChunk{
					Text:     chunkText,
					Page:     pageNum,
					ChunkID:  fmt.Sprintf("%s_p%d_c%d", filename, pageNum, chunkID),
					Document: filename,
					Metadata: map[string]interface{}{
						"page":       pageNum,
						"chunk_id":   chunkID,
						"filename":   filename,
						"word_count": len(strings.Fields(chunkText)),
					},
				}
				chunks = append(chunks, chunk)
				chunkID++

				// Prepare next chunk with overlap
				if i < len(words)-1 {
					overlapWords := strings.Fields(chunkText)
					overlapStart := len(overlapWords) - overlapSize/10 // approximate word count for overlap
					if overlapStart < 0 {
						overlapStart = 0
					}
					if overlapStart < len(overlapWords) {
						currentChunk.Reset()
						currentChunk.WriteString(strings.Join(overlapWords[overlapStart:], " "))
						currentChunk.WriteString(" ")
					} else {
						currentChunk.Reset()
					}
				}
			} else {
				currentChunk.Reset()
			}
		}
	}

	return chunks
}

func (p *PDFProcessor) ProcessPDFFromReader(reader io.Reader, filename string) ([]PDFChunk, error) {
	pdfData, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read PDF data: %w", err)
	}

	return p.ExtractTextFromPDF(pdfData, filename)
}
