package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const defaultHomeDir = "/var/lib/payram-mcp"

const (
	chatBinaryName = "payram-analytics-chat"
	mcpBinaryName  = "payram-analytics-mcp"
	botBinaryName  = "payram-analytics-telegram-bot"
)

// HomeDir resolves the agent home directory from PAYRAM_AGENT_HOME or default.
func HomeDir() string {
	if v := os.Getenv("PAYRAM_AGENT_HOME"); v != "" {
		return v
	}
	return defaultHomeDir
}

// ReleasesDir returns the releases directory.
func ReleasesDir() string { return filepath.Join(HomeDir(), "releases") }

// ReleaseDir returns the directory for a specific version.
func ReleaseDir(version string) string { return filepath.Join(ReleasesDir(), version) }

// StateDir returns the state directory.
func StateDir() string { return filepath.Join(HomeDir(), "state") }

// LockDir returns the lock directory.
func LockDir() string { return filepath.Join(HomeDir(), "lock") }

// LockFilePath returns the update lock path.
func LockFilePath() string { return filepath.Join(LockDir(), "update.lock") }

// CurrentSymlink returns the current symlink path.
func CurrentSymlink() string { return filepath.Join(HomeDir(), "current") }

// PreviousSymlink returns the previous symlink path.
func PreviousSymlink() string { return filepath.Join(HomeDir(), "previous") }

// EnsureBaseDirs ensures required directories exist.
func EnsureBaseDirs() error {
	for _, dir := range []string{ReleasesDir(), StateDir(), LockDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

var ErrUpdateInProgress = errors.New("update already in progress")

// AcquireUpdateLock obtains an exclusive update lock, writing pid/timestamp into the file.
func AcquireUpdateLock() (func() error, error) {
	if err := EnsureBaseDirs(); err != nil {
		return nil, err
	}

	path := LockFilePath()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil, ErrUpdateInProgress
		}
		return nil, err
	}

	_, _ = fmt.Fprintf(f, "pid=%d\nstarted=%s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	_ = f.Close()

	unlock := func() error { return os.Remove(path) }
	return unlock, nil
}

// UpdateSymlinks atomically sets current to newTarget and previous to the old current target.
func UpdateSymlinks(newTarget string) (string, error) {
	if err := EnsureBaseDirs(); err != nil {
		return "", err
	}

	current := CurrentSymlink()
	previous := PreviousSymlink()

	oldTarget, _ := os.Readlink(current)

	if oldTarget != "" {
		tmpPrev := previous + ".tmp"
		_ = os.Remove(tmpPrev)
		if err := os.Symlink(oldTarget, tmpPrev); err != nil {
			return oldTarget, err
		}
		if err := os.Rename(tmpPrev, previous); err != nil {
			_ = os.Remove(tmpPrev)
			return oldTarget, err
		}
	}

	tmpCur := current + ".tmp"
	_ = os.Remove(tmpCur)
	if err := os.Symlink(newTarget, tmpCur); err != nil {
		return oldTarget, err
	}
	if err := os.Rename(tmpCur, current); err != nil {
		_ = os.Remove(tmpCur)
		return oldTarget, err
	}

	return oldTarget, nil
}

// VersionFromTarget extracts the version directory name from a symlink target.
func VersionFromTarget(target string) string {
	if target == "" {
		return ""
	}
	return filepath.Base(target)
}

// DefaultChatBin returns the default chat binary path inside the current release.
func DefaultChatBin() string {
	return filepath.Join(CurrentSymlink(), chatBinaryName)
}

// DefaultMCPBin returns the default MCP binary path inside the current release.
func DefaultMCPBin() string {
	return filepath.Join(CurrentSymlink(), mcpBinaryName)
}

// DefaultBotBin returns the default Telegram bot binary path inside the current release.
func DefaultBotBin() string {
	return filepath.Join(CurrentSymlink(), botBinaryName)
}

// EnsureCompatSymlinks creates compatibility symlinks (chat, mcp) pointing to the canonical binaries.
func EnsureCompatSymlinks(releaseDir string) error {
	links := []struct {
		name   string
		target string
	}{
		{name: "chat", target: chatBinaryName},
		{name: "mcp", target: mcpBinaryName},
		{name: "bot", target: botBinaryName},
	}

	for _, l := range links {
		targetPath := filepath.Join(releaseDir, l.target)
		if _, err := os.Stat(targetPath); err != nil {
			// bot is optional; skip missing target
			if l.name == "bot" {
				continue
			}
			return fmt.Errorf("compat target missing %s: %w", targetPath, err)
		}
		linkPath := filepath.Join(releaseDir, l.name)
		_ = os.Remove(linkPath)
		if err := os.Symlink(targetPath, linkPath); err != nil {
			return fmt.Errorf("create compat symlink %s: %w", linkPath, err)
		}
	}
	return nil
}

// EnsureSeedRelease creates an initial 0.0.0 release with chat/mcp binaries when no current symlink exists.
// It is idempotent: if a valid current symlink already exists, it returns (false, "", nil).
func EnsureSeedRelease(ctx context.Context, home string) (bool, string, error) {
	if home == "" {
		home = HomeDir()
	}

	current := filepath.Join(home, "current")
	if info, err := os.Lstat(current); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			if _, err := os.Readlink(current); err == nil {
				return false, "", nil
			}
		}
	}

	releaseDir := filepath.Join(home, "releases", "0.0.0")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		return false, "", err
	}

	chatSrc := os.Getenv("PAYRAM_AGENT_SEED_CHAT_SRC")
	if chatSrc == "" {
		chatSrc = "/app/chat"
	}
	mcpSrc := os.Getenv("PAYRAM_AGENT_SEED_MCP_SRC")
	if mcpSrc == "" {
		mcpSrc = "/app/mcp"
	}

	chatDst := filepath.Join(releaseDir, chatBinaryName)
	mcpDst := filepath.Join(releaseDir, mcpBinaryName)
	botDst := filepath.Join(releaseDir, botBinaryName)

	if err := copyFileWithMode(chatSrc, chatDst, 0o755); err != nil {
		return false, "", fmt.Errorf("seed chat copy: %w", err)
	}
	if err := copyFileWithMode(mcpSrc, mcpDst, 0o755); err != nil {
		return false, "", fmt.Errorf("seed mcp copy: %w", err)
	}

	botSrc := os.Getenv("PAYRAM_AGENT_SEED_BOT_SRC")
	if botSrc == "" {
		botSrc = "/app/telegram-bot"
	}
	if err := copyOptionalFile(botSrc, botDst, 0o755); err != nil {
		return false, "", fmt.Errorf("seed bot copy: %w", err)
	}
	if err := EnsureCompatSymlinks(releaseDir); err != nil {
		return false, "", err
	}

	oldHome := os.Getenv("PAYRAM_AGENT_HOME")
	_ = os.Setenv("PAYRAM_AGENT_HOME", home)
	defer func() {
		if oldHome == "" {
			_ = os.Unsetenv("PAYRAM_AGENT_HOME")
		} else {
			_ = os.Setenv("PAYRAM_AGENT_HOME", oldHome)
		}
	}()

	if _, err := UpdateSymlinks(releaseDir); err != nil {
		return false, "", err
	}

	status, err := LoadStatus()
	if err == nil && status.CurrentVersion == "" {
		status.CurrentVersion = "0.0.0"
		_ = SaveStatus(status)
	}

	return true, "0.0.0", nil
}

func copyFileWithMode(src, dst string, mode os.FileMode) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	tmp := dst + ".tmp"
	_ = os.Remove(tmp)
	dstFile, err := os.Create(tmp)
	if err != nil {
		return err
	}

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		dstFile.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := dstFile.Chmod(mode); err != nil {
		dstFile.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := dstFile.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	_ = os.Remove(dst)
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	return nil
}

// copyOptionalFile copies src to dst if src exists; missing src is not an error.
func copyOptionalFile(src, dst string, mode os.FileMode) error {
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return copyFileWithMode(src, dst, mode)
}
