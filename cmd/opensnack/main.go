// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package main

import (
	"net/http"
	"time"

	"opensnack/internal/db"
	"opensnack/internal/logging"
	"opensnack/internal/resource"
	"opensnack/internal/router"

	"go.uber.org/zap"
)

func main() {
	logger, err := logging.New()
	if err != nil {
		panic(err)
	}
	zap.ReplaceGlobals(logger)
	defer logger.Sync()

	pg := db.Connect() // returns *gorm.DB
	store := resource.NewGormStore(pg)

	handler := router.New(store)

	srv := &http.Server{
		Addr:           ":4566",
		Handler:        handler,
		ReadTimeout:    15 * time.Second,
		WriteTimeout:   15 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	if err := srv.ListenAndServe(); err != nil {
		zap.L().Fatal("http server exited",
			zap.Error(err),
		)
	}
	zap.L().Info("server started on :4566")

}
