// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"opensnack/internal/logging"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func Connect() *gorm.DB {
	dsn_from_envvar := os.Getenv("OPENSNACK_PG_DSN")
	if dsn_from_envvar == "" {
		panic("OPENSNACK_PG_DSN environment variable is not set")
	}

	db, err := gorm.Open(postgres.Open(dsn_from_envvar), &gorm.Config{
		Logger: logging.NewGormLogger(logger.Warn),
	})
	if err != nil {
		panic(err)
	}

	// Get the underlying *sql.DB to configure connection pool
	sqlDB, err := db.DB()
	if err != nil {
		panic(err)
	}

	// SetMaxIdleConns sets the maximum number of connections in the idle connection pool
	sqlDB.SetMaxIdleConns(10)

	// SetMaxOpenConns sets the maximum number of open connections to the database
	sqlDB.SetMaxOpenConns(100)

	// SetConnMaxLifetime sets the maximum amount of time a connection may be reused
	sqlDB.SetConnMaxLifetime(time.Hour)

	// SetConnMaxIdleTime sets the maximum amount of time a connection may be idle
	sqlDB.SetConnMaxIdleTime(10 * time.Minute)

	return db
}
