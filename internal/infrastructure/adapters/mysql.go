package adapters

import (
	"database/sql"
	"fmt"
	"log"

	"rag-service/internal/infrastructure/config"

	_ "github.com/go-sql-driver/mysql"
)

type MySQLAdapter struct {
	DB *sql.DB
}

func NewMySQLAdapter(cfg *config.Config) (*MySQLAdapter, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.MySQLUser,
		cfg.MySQLPassword,
		cfg.MySQLHost,
		cfg.MySQLPort,
		cfg.MySQLDatabase,
	)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open MySQL connection: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping MySQL: %w", err)
	}

	log.Println("✅ MySQL connected successfully")

	return &MySQLAdapter{DB: db}, nil
}

func (m *MySQLAdapter) Close() error {
	if m.DB != nil {
		return m.DB.Close()
	}
	return nil
}

func (m *MySQLAdapter) HealthCheck() error {
	return m.DB.Ping()
}
