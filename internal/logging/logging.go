// Package logging provides file-based logging to avoid polluting the TUI
package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	logFile       *os.File
	logger        *log.Logger
	mu            sync.Mutex
	logPath       string
	discardLogger *log.Logger
)

func init() {
	// Create a discard logger for when logging is not initialized
	discardLogger = log.New(io.Discard, "", 0)
}

// Init initializes the logging system
func Init(dir string) error {
	mu.Lock()
	defer mu.Unlock()

	// Create log directory if needed
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// Create log file with timestamp
	filename := fmt.Sprintf("rekord_%s.log", time.Now().Format("2006-01-02_15-04-05"))
	logPath = filepath.Join(dir, filename)

	var err error
	logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	logger = log.New(logFile, "", log.LstdFlags|log.Lmicroseconds)
	logger.Printf("Rekord logging initialized")

	return nil
}

// Close closes the log file
func Close() error {
	mu.Lock()
	defer mu.Unlock()

	if logFile != nil {
		logger.Printf("Rekord logging closed")
		err := logFile.Close()
		logFile = nil
		logger = nil
		return err
	}
	return nil
}

// GetLogPath returns the current log file path
func GetLogPath() string {
	mu.Lock()
	defer mu.Unlock()
	return logPath
}

// GetLogger returns the logger instance
func GetLogger() *log.Logger {
	mu.Lock()
	defer mu.Unlock()
	if logger == nil {
		return discardLogger
	}
	return logger
}

// GetLogFile returns the log file for redirecting external command output
func GetLogFile() *os.File {
	mu.Lock()
	defer mu.Unlock()
	return logFile
}

// Info logs an info message
func Info(format string, args ...any) {
	l := GetLogger()
	l.Printf("[INFO] "+format, args...)
}

// Error logs an error message
func Error(format string, args ...any) {
	l := GetLogger()
	l.Printf("[ERROR] "+format, args...)
}

// Debug logs a debug message
func Debug(format string, args ...any) {
	l := GetLogger()
	l.Printf("[DEBUG] "+format, args...)
}

// Warn logs a warning message
func Warn(format string, args ...any) {
	l := GetLogger()
	l.Printf("[WARN] "+format, args...)
}
