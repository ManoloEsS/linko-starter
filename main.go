package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ManoloEsS/linko/internal/build_config"
	"github.com/ManoloEsS/linko/internal/linkoerr"
	"github.com/ManoloEsS/linko/internal/store"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	"github.com/natefinch/lumberjack"
	pkgerr "github.com/pkg/errors"
)

type closeLogger func() error

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}

type multiError interface {
	error
	Unwrap() []error
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
	logger, closeLogger, err := initializeLogger(os.Getenv("LINKO_LOG_FILE"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		return 1
	}
	hostname, _ := os.Hostname()
	logger = logger.With(
		slog.String("git_sha", build_config.GitSHA),
		slog.String("build_time", build_config.BuildTime),
		slog.String("env", os.Getenv("ENV")),
		slog.String("hostname", hostname),
	)

	defer func() {
		if err := closeLogger(); err != nil {
			fmt.Fprintf(os.Stderr, "logger shutdown with error: %v\n", err)
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

	logger.Debug("Linko is shutting down")
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

// func initializeLogger(logFile string) (*slog.Logger, closeLogger, error) {
// 	if logFile != "" {
// 		file, err := os.OpenFile(logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
// 		if err != nil {
// 			return nil, nil, fmt.Errorf("failed to open log file: %v", err)
// 		}
// 		bufferedWriter := bufio.NewWriterSize(file, 8192)
// 		multiWriter := io.MultiWriter(os.Stderr, bufferedWriter)
// 		closeLogger := func() error {
// 			err := bufferedWriter.Flush()
// 			if err != nil {
// 				return err
// 			}
// 			file.Close()
// 			return nil
// 		}
// 		return slog.New(slog.NewTextHandler(multiWriter, &slog.HandlerOptions{
// 			Level: slog.LevelInfo,
// 		})), closeLogger, nil
// 	}
// 	noOp := func() error {
// 		return nil
// 	}
// 	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
// 		Level: slog.LevelDebug,
// 	})), noOp, nil
// }

func initializeLogger(logFile string) (*slog.Logger, closeLogger, error) {
	var (
		handlers []slog.Handler
		closers  []closeLogger
	)

	handlers = append(handlers, tint.NewHandler(os.Stderr, &tint.Options{
		Level:       slog.LevelDebug,
		ReplaceAttr: replaceAttr,
		NoColor:     !(isatty.IsCygwinTerminal(os.Stderr.Fd()) || isatty.IsTerminal(os.Stderr.Fd())),
	}))

	if logFile != "" {
		logRotator := &lumberjack.Logger{
			Filename:   logFile,
			MaxSize:    1,
			MaxAge:     28,
			MaxBackups: 10,
			LocalTime:  false,
			Compress:   true,
		}

		handlers = append(handlers, slog.NewJSONHandler(logRotator, &slog.HandlerOptions{
			Level:       slog.LevelInfo,
			ReplaceAttr: replaceAttr,
		}))

		closers = append(closers, func() error {
			err := logRotator.Close()
			if err != nil {
				return fmt.Errorf("failed to close log file: %w", err)
			}
			return nil
		})
	}

	close := func() error {
		var errs []error
		for _, closer := range closers {
			errs = append(errs, closer())
		}
		return errors.Join(errs...)
	}

	logger := slog.New(slog.NewMultiHandler(handlers...))

	return logger, close, nil
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "error" {
		err, ok := a.Value.Any().(error)
		if !ok {
			return a
		}
		if multiErr, ok := errors.AsType[multiError](err); ok {
			var errs []slog.Attr
			for i, err := range multiErr.Unwrap() {

				errs = append(errs, slog.GroupAttrs(fmt.Sprintf("error_%d", i+1), errorAttrs(err)...))
			}
			return slog.GroupAttrs("errors", errs...)
		}
		return slog.GroupAttrs("error", errorAttrs(err)...)
	}
	return a
}

func errorAttrs(err error) []slog.Attr {
	var errorAttrs []slog.Attr
	errorAttrs = append(errorAttrs, slog.String("message", err.Error()))

	errorAttrs = append(errorAttrs, linkoerr.Attrs(err)...)

	if stackErr, ok := errors.AsType[stackTracer](err); ok {
		errorAttrs = append(errorAttrs, slog.Any("stack_trace", stackErr.StackTrace()))
	}

	return errorAttrs
}
