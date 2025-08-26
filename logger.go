package ayu

import (
	"fmt"
	"log/slog"
)

// Logger is a logger interface
type Logger interface {
	Debugf(template string, args ...interface{})
	Infof(template string, args ...interface{})
	Warnf(template string, args ...interface{})
	Errorf(template string, args ...interface{})
}

type defaultLogger struct{}

func (l *defaultLogger) Debugf(template string, args ...interface{}) {
	slog.Debug(fmt.Sprintf(template, args...))
}

func (l *defaultLogger) Infof(template string, args ...interface{}) {
	slog.Info(fmt.Sprintf(template, args...))
}

func (l *defaultLogger) Warnf(template string, args ...interface{}) {
	slog.Warn(fmt.Sprintf(template, args...))
}

func (l *defaultLogger) Errorf(template string, args ...interface{}) {
	slog.Error(fmt.Sprintf(template, args...))
}

func newDefaultLogger() *defaultLogger {
	return &defaultLogger{}
}
