
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/user/stremio-bitgraph-go/internal/addon"
	"github.com/user/stremio-bitgraph-go/internal/api"
	"github.com/user/stremio-bitgraph-go/internal/config"
	"github.com/user/stremio-bitgraph-go/internal/db"
	"github.com/user/stremio-bitgraph-go/internal/utils"
)

func main() {
	ctx := context.Background()
	if err := db.InitDB(ctx); err != nil {
		utils.Logger.Error("failed to initialize database", "error", err)
		os.Exit(1)
	}
	defer db.Pool.Close()

	// Create a unified root router
	r := chi.NewRouter()
	
	// Mount API sub-router under its specific addon-id path prefix.
	// This prevents the duplicate '/' mount panic.
	r.Mount("/"+config.AddonID, api.NewRouter())
	
	// Mount the core addon sub-router on the root path
	r.Mount("/", addon.NewRouter())

	// Start unified server
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", config.Port),
		Handler: r,
	}

	go func() {
		utils.Logger.Info("unified server listening", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			utils.Logger.Error("server error", "error", err)
		}
	}()

	utils.Logger.Info("addon ready", "manifest", fmt.Sprintf("%s/manifest.json", config.AppHost))

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server.Shutdown(shutdownCtx)
	utils.Logger.Info("server shut down gracefully")
}
