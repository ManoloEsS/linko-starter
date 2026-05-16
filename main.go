package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"
)

type closeLogger func() error

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
	debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level:       slog.LevelDebug,
		ReplaceAttr: replaceAttr,
	})

	file, err := os.OpenFile(logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open log file: %v\n", err)
	}

	bufferedWriter := bufio.NewWriterSize(file, 8192)
	closeLogger := func() error {
		err := bufferedWriter.Flush()
		if err != nil {
			return err
		}
		file.Close()
		return nil
	}

	infoHandler := slog.NewJSONHandler(bufferedWriter, &slog.HandlerOptions{
		Level:       slog.LevelInfo,
		ReplaceAttr: replaceAttr,
	})

	logger := slog.New(slog.NewMultiHandler(
		debugHandler,
		infoHandler,
	))

	return logger, closeLogger, nil
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "error" {
		err, ok := a.Value.Any().(error)
		if !ok {
			return a
		}
		return slog.String("error", fmt.Sprintf("%+v", err))
	}
	return a
}
