package logger

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/eyrihe999-stack/Synapse/config"
	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

// LoggerInterface defines the logging contract
type LoggerInterface interface {
	Debug(message string, fields map[string]interface{})
	Info(message string, fields map[string]interface{})
	Warn(message string, fields map[string]interface{})
	Error(message string, err error, fields map[string]interface{})
	Fatal(message string, err error, fields map[string]interface{})
	Close() error

	DebugCtx(ctx context.Context, message string, fields map[string]interface{})
	InfoCtx(ctx context.Context, message string, fields map[string]interface{})
	WarnCtx(ctx context.Context, message string, fields map[string]interface{})
	ErrorCtx(ctx context.Context, message string, err error, fields map[string]interface{})
	FatalCtx(ctx context.Context, message string, err error, fields map[string]interface{})
}

// Logger implements the LoggerInterface using logrus
type Logger struct {
	logger *logrus.Logger
	config *config.LogConfig
}

// NewLogger creates a new logger instance
func NewLogger(cfg *config.LogConfig) (LoggerInterface, error) {
	l := logrus.New()

	level, err := logrus.ParseLevel(cfg.Level)
	if err != nil {
		return nil, fmt.Errorf("invalid log level '%s': %w", cfg.Level, err)
	}
	l.SetLevel(level)

	switch cfg.Format {
	case "json":
		l.SetFormatter(&logrus.JSONFormatter{TimestampFormat: time.RFC3339})
	default:
		l.SetFormatter(&logrus.TextFormatter{FullTimestamp: true, TimestampFormat: time.RFC3339})
	}

	output, err := getLogOutput(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to setup log output: %w", err)
	}
	l.SetOutput(output)

	return &Logger{logger: l, config: cfg}, nil
}

func getLogOutput(cfg *config.LogConfig) (io.Writer, error) {
	switch cfg.Output {
	case "stdout":
		return os.Stdout, nil
	case "stderr":
		return os.Stderr, nil
	case "file":
		if cfg.FilePath == "" {
			return nil, fmt.Errorf("file path is required when output is 'file'")
		}
		if err := os.MkdirAll(filepath.Dir(cfg.FilePath), 0755); err != nil {
			return nil, fmt.Errorf("failed to create log directory: %w", err)
		}
		return &lumberjack.Logger{
			Filename:   cfg.FilePath,
			MaxSize:    cfg.MaxSize,
			MaxAge:     cfg.MaxAge,
			MaxBackups: cfg.MaxBackups,
			Compress:   cfg.Compress,
		}, nil
	default:
		return os.Stdout, nil
	}
}

func (l *Logger) Debug(message string, fields map[string]interface{}) {
	l.logger.WithFields(logrus.Fields(fields)).Debug(message)
}

func (l *Logger) Info(message string, fields map[string]interface{}) {
	l.logger.WithFields(logrus.Fields(fields)).Info(message)
}

func (l *Logger) Warn(message string, fields map[string]interface{}) {
	l.logger.WithFields(logrus.Fields(fields)).Warn(message)
}

func (l *Logger) Error(message string, err error, fields map[string]interface{}) {
	if fields == nil {
		fields = make(map[string]interface{})
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	l.logger.WithFields(logrus.Fields(fields)).Error(message)
}

func (l *Logger) Fatal(message string, err error, fields map[string]interface{}) {
	if fields == nil {
		fields = make(map[string]interface{})
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	l.logger.WithFields(logrus.Fields(fields)).Fatal(message)
}

func (l *Logger) Close() error { return nil }

// Ctx variants delegate to non-Ctx (no tracing in Nexus)
func (l *Logger) DebugCtx(_ context.Context, message string, fields map[string]interface{}) {
	l.Debug(message, fields)
}
func (l *Logger) InfoCtx(_ context.Context, message string, fields map[string]interface{}) {
	l.Info(message, fields)
}
func (l *Logger) WarnCtx(_ context.Context, message string, fields map[string]interface{}) {
	l.Warn(message, fields)
}
func (l *Logger) ErrorCtx(_ context.Context, message string, err error, fields map[string]interface{}) {
	l.Error(message, err, fields)
}
func (l *Logger) FatalCtx(_ context.Context, message string, err error, fields map[string]interface{}) {
	l.Fatal(message, err, fields)
}

// SimpleLogger provides a simple implementation for basic logging
type SimpleLogger struct{}

func NewSimpleLogger() LoggerInterface { return &SimpleLogger{} }

func (l *SimpleLogger) Debug(message string, fields map[string]interface{}) {
	l.log("DEBUG", message, fields)
}
func (l *SimpleLogger) Info(message string, fields map[string]interface{}) {
	l.log("INFO", message, fields)
}
func (l *SimpleLogger) Warn(message string, fields map[string]interface{}) {
	l.log("WARN", message, fields)
}
func (l *SimpleLogger) Error(message string, err error, fields map[string]interface{}) {
	if fields == nil {
		fields = make(map[string]interface{})
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	l.log("ERROR", message, fields)
}
func (l *SimpleLogger) Fatal(message string, err error, fields map[string]interface{}) {
	if fields == nil {
		fields = make(map[string]interface{})
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	l.log("FATAL", message, fields)
	os.Exit(1)
}
func (l *SimpleLogger) Close() error { return nil }

func (l *SimpleLogger) DebugCtx(_ context.Context, message string, fields map[string]interface{}) {
	l.Debug(message, fields)
}
func (l *SimpleLogger) InfoCtx(_ context.Context, message string, fields map[string]interface{}) {
	l.Info(message, fields)
}
func (l *SimpleLogger) WarnCtx(_ context.Context, message string, fields map[string]interface{}) {
	l.Warn(message, fields)
}
func (l *SimpleLogger) ErrorCtx(_ context.Context, message string, err error, fields map[string]interface{}) {
	l.Error(message, err, fields)
}
func (l *SimpleLogger) FatalCtx(_ context.Context, message string, err error, fields map[string]interface{}) {
	l.Fatal(message, err, fields)
}

func (l *SimpleLogger) log(level, message string, fields map[string]interface{}) {
	timestamp := time.Now().Format(time.RFC3339)
	logMsg := fmt.Sprintf("[%s] %s: %s", timestamp, level, message)
	if len(fields) > 0 {
		logMsg += " |"
		for key, value := range fields {
			logMsg += fmt.Sprintf(" %s=%v", key, value)
		}
	}
	log.Println(logMsg)
}

func GetLogger(cfg *config.LogConfig) (LoggerInterface, error) {
	if cfg == nil {
		return NewSimpleLogger(), nil
	}
	return NewLogger(cfg)
}

// WithContext returns a ContextLogger that wraps the logger with context
func WithContext(logger LoggerInterface, ctx context.Context) *ContextLogger {
	return &ContextLogger{logger: logger, ctx: ctx}
}

// ContextLogger wraps a logger with a context
type ContextLogger struct {
	logger LoggerInterface
	ctx    context.Context
}

func (cl *ContextLogger) Debug(message string, fields map[string]interface{}) {
	cl.logger.DebugCtx(cl.ctx, message, fields)
}
func (cl *ContextLogger) Info(message string, fields map[string]interface{}) {
	cl.logger.InfoCtx(cl.ctx, message, fields)
}
func (cl *ContextLogger) Warn(message string, fields map[string]interface{}) {
	cl.logger.WarnCtx(cl.ctx, message, fields)
}
func (cl *ContextLogger) Error(message string, err error, fields map[string]interface{}) {
	cl.logger.ErrorCtx(cl.ctx, message, err, fields)
}
func (cl *ContextLogger) Fatal(message string, err error, fields map[string]interface{}) {
	cl.logger.FatalCtx(cl.ctx, message, err, fields)
}
