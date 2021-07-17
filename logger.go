package ayu

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Logger interface {
	Debugf(template string, args ...interface{})
	Debugw(template string, keysAndValues ...interface{})
	Infof(template string, args ...interface{})
	Infow(template string, keysAndValues ...interface{})
	Warnf(template string, args ...interface{})
	Warnw(template string, keysAndValues ...interface{})
	Errorf(template string, args ...interface{})
	Errorw(template string, keysAndValues ...interface{})
	Fatalf(template string, args ...interface{})
	Fatalw(template string, keysAndValues ...interface{})
}

func NewDefaultLogger() (*zap.SugaredLogger, error) {
	config := zap.NewDevelopmentConfig()
	config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	zapLogger, err := config.Build(zap.AddStacktrace(zap.FatalLevel))
	if err != nil {
		return nil, err
	}
	return zapLogger.Sugar(), nil
}
