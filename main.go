package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"
	pkgerrors "github.com/pkg/errors"
)

type closeFunc func() error

type stackTracer interface {
	error
	StackTrace() pkgerrors.StackTrace
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "error" {
		err, ok := a.Value.Any().(error)
		if !ok {
			return a
		}

		if stackErr, ok := errors.AsType[stackTracer](err); ok {
			return slog.GroupAttrs("error", slog.Attr{
				Key:   "message",
				Value: slog.StringValue(stackErr.Error()),
			}, slog.Attr{
				Key:   "stack_trace",
				Value: slog.StringValue(fmt.Sprintf("%+v", stackErr.StackTrace())),
			})
		}
		return slog.String("error", fmt.Sprintf("%+v", err))
	}
	return a
}

func initializeLogger() (*slog.Logger, closeFunc, error) {
	logFileLocation := os.Getenv("LINKO_LOG_FILE")

	var logger *slog.Logger

	debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level:       slog.LevelDebug,
		ReplaceAttr: replaceAttr,
	})

	if logFileLocation == "" {
		logger = slog.New(debugHandler)

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

		infoHandler := slog.NewJSONHandler(bufferedFile, &slog.HandlerOptions{
			Level:       slog.LevelInfo,
			ReplaceAttr: replaceAttr,
		})

		logger = slog.New(slog.NewMultiHandler(infoHandler, debugHandler))

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
			fmt.Fprintf(os.Stderr, "failed to close logger: %v\n", err)
		}
	}()

	st, err := store.New(dataDir, logger)

	if err != nil {
		logger.Error("failed to create store", "error", err)
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
		logger.Error("failed to shutdown server", "error", err)
		return 1
	}

	if serverErr != nil {
		logger.Error("server error", "error", serverErr)
		return 1
	}
	return 0
}
