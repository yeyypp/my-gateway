package log

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var logger = zap.NewNop()

func Init(level string, outpaths ...string) error {
	cfg := zap.NewProductionConfig()

	lvl, err := zapcore.ParseLevel(level)
	if err != nil {
		return err
	}
	cfg.Level = zap.NewAtomicLevelAt(lvl)

	if len(outpaths) > 0 {
		cfg.OutputPaths = outpaths
	}

	newLogger, err := cfg.Build(zap.AddCallerSkip(1))
	if err != nil {
		return err
	}

	logger = newLogger
	return nil
}

func Info(msg string, fields ...zap.Field) {
	logger.Info(msg, fields...)
}

func Error(msg string, fields ...zap.Field) {
	logger.Error(msg, fields...)
}

func Panic(msg string, fields ...zap.Field) {
	logger.Panic(msg, fields...)
}

func Infof(template string, args ...any) {
	logger.Sugar().Infof(template, args...)
}

func Errorf(template string, args ...any) {
	logger.Sugar().Errorf(template, args...)
}

func Panicf(template string, args ...any) {
	logger.Sugar().Panicf(template, args...)
}

func Sync() error {
	return logger.Sync()
}
