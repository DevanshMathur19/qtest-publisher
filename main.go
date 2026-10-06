package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/harness-community/qtest-publisher/plugin"
)

var version = "dev"

func main() {
	logger := log.New(os.Stdout, "", 0)
	cfg, err := plugin.LoadConfig()
	if err != nil {
		logger.Printf("qTest publisher configuration error: %v", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(
		context.Background(), os.Interrupt, syscall.SIGTERM,
	)
	defer cancel()
	ctx, timeoutCancel := context.WithTimeout(ctx, cfg.Timeout)
	defer timeoutCancel()

	logger.Printf("qTest publisher %s starting", version)
	if _, err := plugin.Run(ctx, cfg, logger); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			logger.Printf("qTest publisher cancelled: %v", err)
		} else {
			logger.Printf("qTest publisher failed: %v", err)
		}
		os.Exit(1)
	}
}
