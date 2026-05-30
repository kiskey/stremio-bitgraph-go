package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	// Start API server
	apiServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", config.APIPort),
		Handler: api.NewRouter(),
	}
	go func() {
		utils.Logger.Info("API server listening", "addr", apiServer.Addr)
		if err := apiServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			utils.Logger.Error("API server error", "error", err)
		}
	}()

	// Start addon server
	addonServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", config.Port),
		Handler: addon.NewRouter(),
	}
	go func() {
		utils.Logger.Info("addon server listening", "addr", addonServer.Addr)
		if err := addonServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			utils.Logger.Error("addon server error", "error", err)
		}
	}()

	utils.Logger.Info("addon ready", "manifest", fmt.Sprintf("%s/manifest.json", config.AppHost))

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	apiServer.Shutdown(shutdownCtx)
	addonServer.Shutdown(shutdownCtx)
	utils.Logger.Info("servers shut down gracefully")
}
