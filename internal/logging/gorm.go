// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package logging

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type GormLogger struct {
	logLevel logger.LogLevel
}

func NewGormLogger(level logger.LogLevel) logger.Interface {
	return &GormLogger{logLevel: level}
}

func (l *GormLogger) LogMode(level logger.LogLevel) logger.Interface {
	return &GormLogger{logLevel: level}
}

func (l *GormLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	if l.logLevel >= logger.Info {
		zap.L().Sugar().Infof(msg, data...)
	}
}

func (l *GormLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	if l.logLevel >= logger.Warn {
		zap.L().Sugar().Warnf(msg, data...)
	}
}

func (l *GormLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	if l.logLevel >= logger.Error {
		zap.L().Sugar().Errorf(msg, data...)
	}
}

func (l *GormLogger) Trace(
	ctx context.Context,
	begin time.Time,
	fc func() (string, int64),
	err error,
) {
	if l.logLevel <= logger.Silent {
		return
	}

	sql, rows := fc()
	elapsed := time.Since(begin)

	fields := []zap.Field{
		zap.Duration("duration", elapsed),
		zap.Int64("rows", rows),
	}

	if err != nil {
		// expected "record not found" error
		if err == gorm.ErrRecordNotFound {
			zap.L().Debug("gorm query not found",
				zap.String("sql", sql),
				zap.Duration("duration", elapsed),
			)
			return
		}

		// Postgres constraint violations (this is expected in some cases)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			if pgErr.Code == "23505" { // unique_violation
				zap.L().Info("gorm unique constraint violation",
					zap.String("constraint", pgErr.ConstraintName),
					zap.String("sql", sql),
					zap.Duration("duration", elapsed),
					zap.Error(err),
				)
				return
			}
		}

		zap.L().Warn("gorm query failed",
			zap.String("sql", sql),
			zap.Duration("duration", elapsed),
			zap.Error(err),
		)
		return
	}

	zap.L().Debug("gorm query",
		append(fields,
			zap.String("sql", sql),
		)...,
	)
}
