package config

import (
	"os"
	"strconv"
)

type Config struct {
	// Server
	Port string

	// MySQL
	MySQLHost     string
	MySQLPort     string
	MySQLUser     string
	MySQLPassword string
	MySQLDatabase string

	// MinIO
	MinIOEndpoint  string
	MinIOAccessKey string
	MinIOSecretKey string
	MinIOUseSSL    bool

	// Qdrant
	QdrantHost string
	QdrantPort string

	// Ollama
	OllamaHost  string
	OllamaPort  string
	OllamaModel string
}

func Load() *Config {
	useSSL, _ := strconv.ParseBool(getEnv("MINIO_USE_SSL", "false"))

	return &Config{
		// Server
		Port: getEnv("PORT", "8090"),

		// MySQL
		MySQLHost:     getEnv("MYSQL_HOST", "localhost"),
		MySQLPort:     getEnv("MYSQL_PORT", "3306"),
		MySQLUser:     getEnv("MYSQL_USER", "rag_user"),
		MySQLPassword: getEnv("MYSQL_PASSWORD", "rag_password"),
		MySQLDatabase: getEnv("MYSQL_DATABASE", "rag_db"),

		// MinIO
		MinIOEndpoint:  getEnv("MINIO_ENDPOINT", "localhost:9000"),
		MinIOAccessKey: getEnv("MINIO_ACCESS_KEY", "minioadmin"),
		MinIOSecretKey: getEnv("MINIO_SECRET_KEY", "minioadmin123"),
		MinIOUseSSL:    useSSL,

		// Qdrant
		QdrantHost: getEnv("QDRANT_HOST", "localhost"),
		QdrantPort: getEnv("QDRANT_PORT", "6333"),

		// Ollama
		OllamaHost:  getEnv("OLLAMA_HOST", "localhost"),
		OllamaPort:  getEnv("OLLAMA_PORT", "11434"),
		OllamaModel: getEnv("OLLAMA_MODEL", "llama3.2:3b"),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
