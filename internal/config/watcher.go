package config

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watch monitors config files for changes and calls onChange when modified.
// It debounces rapid writes with a 500ms window. Blocks until ctx is cancelled.
// Watches mainPath, localPath (if non-empty), and envFilePath (if non-empty).
func Watch(ctx context.Context, mainPath string, localPath string, envFilePath string, onChange func(), logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	// Watch main config.
	mainPath = expandHome(mainPath)
	if err := watcher.Add(mainPath); err != nil {
		return err
	}

	// Watch local config if it exists (AR-11 fix).
	if localPath != "" {
		localPath = expandHome(localPath)
		if err := watcher.Add(localPath); err != nil {
			logger.Debug("config watcher: local config not watched", "path", localPath, "error", err)
		}
	}

	// Watch env file for changes (reload triggers re-expansion).
	if envFilePath != "" {
		envFilePath = expandHome(envFilePath)
		if err := watcher.Add(envFilePath); err != nil {
			logger.Debug("config watcher: env file not watched", "path", envFilePath, "error", err)
		}
	}

	const debounce = 500 * time.Millisecond
	var timer *time.Timer
	// F-6 / T13A.3-4 fix: serialize onChange invocations. AfterFunc runs
	// the callback on its own goroutine; if two rapid bursts of fsnotify
	// events fire AfterFuncs whose callbacks overlap, onChange could be
	// entered concurrently and produce duplicate reconcile work. Mutex
	// guarantees at-most-one in-flight reconcile; a ctx.Err() check at
	// entry avoids running after shutdown has started.
	var onChangeMu sync.Mutex

	for {
		select {
		case <-ctx.Done():
			// CR-5/AR-4 fix: cancel pending debounce timer on shutdown.
			if timer != nil {
				timer.Stop()
			}
			return ctx.Err()

		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			// Debounce: reset timer on each event.
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(debounce, func() {
				// Exit early if shutdown has begun between timer arm
				// and this callback firing.
				if ctx.Err() != nil {
					return
				}
				onChangeMu.Lock()
				defer onChangeMu.Unlock()
				// Re-check context under the lock — another invocation
				// may have held the mutex through shutdown.
				if ctx.Err() != nil {
					return
				}
				logger.Info("config file changed", "path", event.Name, "op", event.Op.String())
				onChange()
			})

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			logger.Warn("config watcher error", "error", err)
		}
	}
}
