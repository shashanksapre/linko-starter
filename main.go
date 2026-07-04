package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"
)

type closeFunc func() error

func initializeLogger() (*log.Logger, closeFunc, error) {
	logFileLocation := os.Getenv("LINKO_LOG_FILE")

	var logger *log.Logger

	if logFileLocation == "" {
		logger = log.New(os.Stderr, "", log.LstdFlags)

		noOp := func() error {
			return nil
		}

		return logger, noOp, nil
	} else {
		logFile, err := os.OpenFile(logFileLocation, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)

		if err != nil {
			return nil, nil, fmt.Errorf("failed to open log file: %w", err)
		}

		bufferedFile := bufio.NewWriterSize(logFile, 8192)
		multiWriter := io.MultiWriter(os.Stderr, bufferedFile)
		logger = log.New(multiWriter, "", log.LstdFlags)

		closer := func() error {
			err := bufferedFile.Flush()

			if err != nil {
				return err
			}

			err = logFile.Close()

			if err != nil {
				return err
			}

			return nil
		}

		return logger, closer, nil
	}
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
	logger, closer, err := initializeLogger()

	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initalise file logger: %v\n", err)
		return 1
	}

	defer func() {
		err := closer()

		if err != nil {
			fmt.Fprintf(os.Stderr, "some error: %v\n", err)
		}
	}()

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
