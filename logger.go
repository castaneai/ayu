package ayu

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger is a logger interface
type Logger interface {
	Debugf(template string, args ...interface{})
	Infof(template string, args ...interface{})
	Warnf(template string, args ...interface{})
	Errorf(template string, args ...interface{})
}

func newDefaultLogger() (*zap.SugaredLogger, error) {
	config := zap.NewDevelopmentConfig()
	config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	zapLogger, err := config.Build(zap.AddStacktrace(zap.FatalLevel))
	if err != nil {
		return nil, err
	}
	return zapLogger.Sugar(), nil
}
