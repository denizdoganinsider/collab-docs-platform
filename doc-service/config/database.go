package config

import (
	"database/sql"
	"log/slog"
	"os"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func NewDatabase(dsn string) *sql.DB {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}

	// Bounded pool: the compose MySQL (max_connections 151) is shared with two
	// sibling projects; unlimited open connections would turn a burst into
	// "Too many connections" for everyone.
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		slog.Error("ping database", "error", err)
		os.Exit(1)
	}

	slog.Info("doc-service database connected")

	return db
}
