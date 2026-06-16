package logger

import (
	"os"

	"github.com/samvad-hq/samvad-news-harvester/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger describes the logging surface used across the service.
type Logger interface {
	InfoObj(msg, key string, obj interface{})
	DebugObj(msg, key string, obj interface{})
	WarnObj(msg, key string, obj interface{})
	ErrorObj(msg, key string, obj interface{})
	Sync() error
}

type zapLogger struct {
	s *zap.SugaredLogger
}

// NopLogger discards all log messages.
type NopLogger struct{}

func (NopLogger) InfoObj(string, string, interface{})  {}
func (NopLogger) DebugObj(string, string, interface{}) {}
func (NopLogger) WarnObj(string, string, interface{})  {}
func (NopLogger) ErrorObj(string, string, interface{}) {}
func (NopLogger) Sync() error                          { return nil }

var global Logger

// Init initializes the global logger based on the provided config.
func Init(cfg *config.Config) (Logger, error) {
	var level zapcore.Level
	switch cfg.LogLevel {
	case "debug":
		level = zapcore.DebugLevel
	case "info":
		level = zapcore.InfoLevel
	case "warn", "warning":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	default:
		level = zapcore.InfoLevel
	}

	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "ts"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.AddSync(zapcore.Lock(os.Stdout)),
		level,
	)

	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	zLogger := &zapLogger{s: logger.Sugar()}
	global = zLogger
	return zLogger, nil
}

// InfoObj logs an informational message with an associated object.
func (l *zapLogger) InfoObj(msg, key string, obj interface{}) {
	l.s.Desugar().Info(msg, zap.Any(key, obj))
}

// DebugObj logs a debug message with an associated object.
func (l *zapLogger) DebugObj(msg, key string, obj interface{}) {
	l.s.Desugar().Debug(msg, zap.Any(key, obj))
}

// WarnObj logs a warning message with an associated object.
func (l *zapLogger) WarnObj(msg, key string, obj interface{}) {
	l.s.Desugar().Warn(msg, zap.Any(key, obj))
}

// ErrorObj logs an error message with an associated object.
func (l *zapLogger) ErrorObj(msg, key string, obj interface{}) {
	l.s.Desugar().Error(msg, zap.Any(key, obj))
}

// Sync flushes any buffered log entries.
func (l *zapLogger) Sync() error {
	return l.s.Sync()
}

// Close flushes and closes the global logger.
func Close() error {
	if global == nil {
		return nil
	}
	return global.Sync()
}

func InfoObj(msg, key string, obj interface{}) {
	if global == nil {
		return
	}
	global.InfoObj(msg, key, obj)
}

func DebugObj(msg, key string, obj interface{}) {
	if global == nil {
		return
	}
	global.DebugObj(msg, key, obj)
}

func WarnObj(msg, key string, obj interface{}) {
	if global == nil {
		return
	}
	global.WarnObj(msg, key, obj)
}

func ErrorObj(msg, key string, obj interface{}) {
	if global == nil {
		return
	}
	global.ErrorObj(msg, key, obj)
}
