package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"
)

func initializeLogger() *log.Logger {
	logFileLocation := os.Getenv("LINKO_LOG_FILE")

	var logger *log.Logger

	if logFileLocation == "" {
		logger = log.New(os.Stderr, "", log.LstdFlags)
	} else {
		logFile, err := os.OpenFile(logFileLocation, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)

		if err != nil {
			log.Fatalf("failed to open log file: %v", err)
		}

		multiWriter := io.MultiWriter(os.Stderr, logFile)
		logger = log.New(multiWriter, "", log.LstdFlags)
	}

	return logger
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	status := run(ctx, cancel, *httpPort, *dataDir)
	cancel()
	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string) int {
	logger := initializeLogger()
	st, err := store.New(dataDir, logger)

	if err != nil {
		logger.Printf("failed to create store: %v", err)
		return 1
	}
	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Printf("failed to shutdown server: %v", err)
		return 1
	}
	if serverErr != nil {
		logger.Printf("server error: %v", serverErr)
		return 1
	}
	return 0
}
