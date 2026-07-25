package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"boot.dev/linko/internal/build"
	"boot.dev/linko/internal/linkoerr"
	"boot.dev/linko/internal/store"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	pkgerrors "github.com/pkg/errors"
	"gopkg.in/natefinch/lumberjack.v2"
)

type closeFunc func() error

type stackTracer interface {
	error
	StackTrace() pkgerrors.StackTrace
}

type multiError interface {
	error
	Unwrap() []error
}

func redactIP(ipAddr string) string {
	host, _, err := net.SplitHostPort(ipAddr)

	if err != nil {
		return ipAddr
	}

	parsedIP := net.ParseIP(host)

	if parsedIP == nil {
		return ipAddr
	}

	ipV4 := parsedIP.To4()

	if ipV4 != nil {
		_bytes := strings.Split(ipV4.String(), ".")
		return strings.Join(_bytes[0:len(_bytes)-1], ".") + ".x"
	} else {
		return ipAddr
	}
}

func errorAttributesBuilder(err error) []slog.Attr {
	errorAttributes := linkoerr.Attrs(err)

	errorAttributes = append([]slog.Attr{{
		Key:   "message",
		Value: slog.StringValue(err.Error()),
	}}, errorAttributes...)

	if stackErr, ok := errors.AsType[stackTracer](err); ok {
		errorAttributes = append([]slog.Attr{{
			Key:   "stack_trace",
			Value: slog.StringValue(fmt.Sprintf("%+v", stackErr.StackTrace())),
		}}, errorAttributes...)
	}

	return errorAttributes
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {

	var sensitiveKeys = []string{"password", "key", "apikey", "secret", "pin", "creditcardno", "user"}

	if slices.Contains(sensitiveKeys, a.Key) {
		return slog.String(a.Key, "[REDACTED]")
	}

	if a.Key == "error" {
		err, ok := a.Value.Any().(error)

		if !ok {
			return a
		}

		if multiErr, ok := errors.AsType[multiError](err); ok {
			var errAttrs []slog.Attr

			for i, err := range multiErr.Unwrap() {
				var errAttrI []slog.Attr
				errAttrI = append(errAttrI, errorAttributesBuilder(err)...)
				errAttr := slog.GroupAttrs(fmt.Sprintf("error_%d", i+1), errAttrI...)
				errAttrs = append(errAttrs, errAttr)
			}

			return slog.GroupAttrs("errors", errAttrs...)
		} else {
			return slog.GroupAttrs("error", errorAttributesBuilder(err)...)
		}

	}

	stringValue, ok := a.Value.Any().(string)

	if !ok {
		return a
	}

	urlValue, err := url.Parse(stringValue)

	if err != nil || urlValue.User == nil {
		return a
	} else {
		password, ok := urlValue.User.Password()

		if ok {
			redactedStringValue := strings.ReplaceAll(stringValue, password, "[REDACTED]")
			return slog.String(a.Key, redactedStringValue)
		} else {
			return a
		}
	}
}

func initializeLogger() (*slog.Logger, closeFunc, error) {
	env := os.Getenv("ENV")
	hostname, _ := os.Hostname()

	logFileLocation := os.Getenv("LINKO_LOG_FILE")

	var logger *slog.Logger

	debugHandler := tint.NewTextHandler(os.Stderr, &tint.Options{
		Level:       slog.LevelDebug,
		ReplaceAttr: replaceAttr,
		NoColor:     !(isatty.IsCygwinTerminal(os.Stderr.Fd()) || isatty.IsTerminal(os.Stderr.Fd())),
	})

	if logFileLocation == "" {
		logger = slog.New(debugHandler)

		logger = logger.With(
			slog.String("git_sha", build.GitSHA),
			slog.String("build_time", build.BuildTime),
			slog.String("env", env),
			slog.String("hostname", hostname),
		)

		noOp := func() error {
			return nil
		}

		return logger, noOp, nil
	} else {
		lumberLogger := &lumberjack.Logger{
			Filename:   logFileLocation,
			MaxSize:    1,
			MaxAge:     28,
			MaxBackups: 10,
			LocalTime:  false,
			Compress:   true,
		}

		infoHandler := slog.NewJSONHandler(lumberLogger, &slog.HandlerOptions{
			ReplaceAttr: replaceAttr,
		})

		logger = slog.New(slog.NewMultiHandler(infoHandler, debugHandler))

		logger = logger.With(
			slog.String("git_sha", build.GitSHA),
			slog.String("build_time", build.BuildTime),
			slog.String("env", env),
			slog.String("hostname", hostname),
		)
		closer := func() error {
			err := lumberLogger.Close()

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
