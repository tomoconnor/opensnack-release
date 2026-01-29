// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package logging

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

func New() (*zap.Logger, error) {
	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "ts"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	var encoder zapcore.Encoder
	if os.Getenv("LOG_FORMAT") == "json" {
		encoder = zapcore.NewJSONEncoder(encoderCfg)
	} else {
		encoderCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
		encoder = zapcore.NewConsoleEncoder(encoderCfg)

	}

	level := zap.WarnLevel
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = zap.DebugLevel
	}

	fileWriter := zapcore.AddSync(&lumberjack.Logger{
		Filename:   "opensnack.log",
		MaxSize:    100, // MB
		MaxBackups: 5,
		MaxAge:     7,
		Compress:   true,
	})

	core := zapcore.NewTee(
		zapcore.NewCore(encoder, zapcore.AddSync(os.Stdout), level),
		zapcore.NewCore(encoder, fileWriter, level),
	)

	return zap.New(
		core,
		zap.AddCaller(),
		zap.AddStacktrace(zap.FatalLevel),
	), nil
}
