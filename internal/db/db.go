// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"opensnack/internal/logging"
	"os"
	"strconv"
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

	// Keep the pool well under Postgres' max_connections, with room for every
	// replica: the old fixed 100 per process ran the server out of clients
	// under load. Requests queue for a connection instead.
	sqlDB.SetMaxOpenConns(envInt("OPENSNACK_PG_MAX_OPEN", 25))
	sqlDB.SetMaxIdleConns(envInt("OPENSNACK_PG_MAX_IDLE", 10))

	// SetConnMaxLifetime sets the maximum amount of time a connection may be reused
	sqlDB.SetConnMaxLifetime(time.Hour)

	// SetConnMaxIdleTime sets the maximum amount of time a connection may be idle
	sqlDB.SetConnMaxIdleTime(10 * time.Minute)

	return db
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}
