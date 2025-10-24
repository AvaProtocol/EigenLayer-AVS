package logger

import (
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

// Logger is re-exported from eigensdk-go for convenience.
// This allows users of this package to work with loggers without importing sdklogging separately.
type Logger = sdklogging.Logger

// NoOpLogger implements Logger with no-op methods to avoid nil pointer panics.
// Use this when you need a logger instance but don't want any actual logging.
type NoOpLogger struct{}

func (l *NoOpLogger) Info(msg string, keysAndValues ...interface{})  {}
func (l *NoOpLogger) Infof(format string, args ...interface{})       {}
func (l *NoOpLogger) Debug(msg string, keysAndValues ...interface{}) {}
func (l *NoOpLogger) Debugf(format string, args ...interface{})      {}
func (l *NoOpLogger) Error(msg string, keysAndValues ...interface{}) {}
func (l *NoOpLogger) Errorf(format string, args ...interface{})      {}
func (l *NoOpLogger) Warn(msg string, keysAndValues ...interface{})  {}
func (l *NoOpLogger) Warnf(format string, args ...interface{})       {}
func (l *NoOpLogger) Fatal(msg string, keysAndValues ...interface{}) {}
func (l *NoOpLogger) Fatalf(format string, args ...interface{})      {}
func (l *NoOpLogger) With(keysAndValues ...interface{}) Logger       { return l }
func (l *NoOpLogger) WithComponent(componentName string) Logger      { return l }
func (l *NoOpLogger) WithName(name string) Logger                    { return l }
func (l *NoOpLogger) WithServiceName(serviceName string) Logger      { return l }
func (l *NoOpLogger) WithHostName(hostName string) Logger            { return l }
func (l *NoOpLogger) Sync() error                                    { return nil }

// NewNoOpLogger creates a new no-op logger instance
func NewNoOpLogger() Logger {
	return &NoOpLogger{}
}

// EnsureLogger returns the logger if not nil, otherwise returns a no-op logger.
// This is a convenience function to safely use optional logger parameters.
func EnsureLogger(logger Logger) Logger {
	if logger == nil {
		return NewNoOpLogger()
	}
	return logger
}
