package logging

import (
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
)

// New creates a logger that writes to logs/<component>.log and returns it with a cleanup.
func New(component string) (*logrus.Entry, func(), error) {
	logger := logrus.New()
	logger.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})

	if err := os.MkdirAll("logs", 0o755); err != nil {
		// If we can't create logs dir (e.g., in container), write to stdout
		logger.SetOutput(os.Stdout)
		return logger.WithField("component", component), func() {}, nil
	}
	path := filepath.Join("logs", component+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// Fall back to stdout if file can't be opened
		logger.SetOutput(os.Stdout)
		return logger.WithField("component", component), func() {}, nil
	}

	logger.SetOutput(f)
	return logger.WithField("component", component), func() { _ = f.Close() }, nil
}
