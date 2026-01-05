package logger

import (
	"io"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	SystemLog *log.Logger
)

func Init(homeDir string) {
	logDir := filepath.Join(homeDir, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Printf("Failed to create log dir: %v", err)
	}

	logFile := filepath.Join(logDir, "system.log")

	rotator := &lumberjack.Logger{
		Filename:   logFile,
		MaxSize:    10, // megabytes
		MaxBackups: 3,
		MaxAge:     28, // days
		Compress:   true, // compress rotated files
	}

	// MultiWriter: Write to both stdout and file
	// We want stdout for CLI feedback, and file for persistence.
	mw := io.MultiWriter(os.Stdout, rotator)

	SystemLog = log.New(mw, "", log.LstdFlags)
	
	// Override standard library log to capture dependency logs too
	log.SetOutput(mw)
}

// Printf is a helper to use the SystemLog
func Printf(format string, v ...interface{}) {
	if SystemLog != nil {
		SystemLog.Printf(format, v...)
	} else {
		log.Printf(format, v...)
	}
}

// Fatal is a helper
func Fatal(v ...interface{}) {
	if SystemLog != nil {
		SystemLog.Fatal(v...)
	} else {
		log.Fatal(v...)
	}
}

// Fatalf is a helper
func Fatalf(format string, v ...interface{}) {
	if SystemLog != nil {
		SystemLog.Fatalf(format, v...)
	} else {
		log.Fatalf(format, v...)
	}
}
