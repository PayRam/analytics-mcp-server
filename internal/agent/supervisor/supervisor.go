package supervisor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/payram/payram-analytics-mcp-server/internal/agent/secrets"
	"github.com/payram/payram-analytics-mcp-server/internal/agent/update"
)

// Config controls supervisor behavior.
// BufferLines defines how many log lines to keep per child.
// InitialBackoff defines the first delay after a crash; MaxBackoff caps it.
// TerminateTimeout defines how long to wait after SIGTERM before SIGKILL.
type Config struct {
	ChatPath         string
	ChatArgs         []string
	MCPPath          string
	MCPArgs          []string
	BufferLines      int
	InitialBackoff   time.Duration
	MaxBackoff       time.Duration
	TerminateTimeout time.Duration
}

// ExitInfo describes the last exit of a child process.
type ExitInfo struct {
	Time     time.Time `json:"time"`
	ExitCode int       `json:"exitCode,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// ComponentStatus reports the current state of a child.
type ComponentStatus struct {
	Name      string    `json:"name"`
	PID       int       `json:"pid"`
	StartTime time.Time `json:"startTime"`
	Restarts  int       `json:"restarts"`
	LastExit  *ExitInfo `json:"lastExit,omitempty"`
}

// Status aggregates child statuses.
type Status struct {
	Components []ComponentStatus `json:"components"`
}

// Supervisor manages chat and MCP child processes.
type Supervisor struct {
	chat *child
	mcp  *child

	wg sync.WaitGroup
}

var forwardChildLogs = isForwardChildLogsEnabled()

// NewFromEnv builds a Supervisor using environment overrides for binaries.
func NewFromEnv() (*Supervisor, error) {
	chatPath := getenvDefault("PAYRAM_AGENT_CHAT_BIN", update.DefaultChatBin())
	mcpPath := getenvDefault("PAYRAM_AGENT_MCP_BIN", update.DefaultMCPBin())

	if chatPath == update.DefaultChatBin() {
		if _, err := os.Stat(chatPath); err != nil {
			return nil, fmt.Errorf("chat binary not found at %s", chatPath)
		}
	}
	if mcpPath == update.DefaultMCPBin() {
		if _, err := os.Stat(mcpPath); err != nil {
			return nil, fmt.Errorf("mcp binary not found at %s", mcpPath)
		}
	}

	cfg := Config{
		ChatPath:         chatPath,
		MCPPath:          mcpPath,
		BufferLines:      200,
		InitialBackoff:   time.Second,
		MaxBackoff:       30 * time.Second,
		TerminateTimeout: 5 * time.Second,
	}
	return New(cfg), nil
}

// New builds a Supervisor from config.
func New(cfg Config) *Supervisor {
	if cfg.ChatPath == "" {
		cfg.ChatPath = update.DefaultChatBin()
	}
	if cfg.MCPPath == "" {
		cfg.MCPPath = update.DefaultMCPBin()
	}
	if cfg.BufferLines <= 0 {
		cfg.BufferLines = 200
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 30 * time.Second
	}
	if cfg.TerminateTimeout <= 0 {
		cfg.TerminateTimeout = 5 * time.Second
	}

	return &Supervisor{
		chat: newChild("chat", cfg.ChatPath, cfg.ChatArgs, cfg),
		mcp:  newChild("mcp", cfg.MCPPath, cfg.MCPArgs, cfg),
	}
}

// Start launches child supervision loops. Safe to call once.
func (s *Supervisor) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is nil")
	}

	s.wg.Add(2)
	go s.chat.run(ctx, &s.wg)
	go s.mcp.run(ctx, &s.wg)
	return nil
}

// RestartAll forces both children to restart.
func (s *Supervisor) RestartAll() error {
	s.chat.triggerRestart()
	s.mcp.triggerRestart()
	return nil
}

// Status returns aggregate child status.
func (s *Supervisor) Status() Status {
	return Status{Components: []ComponentStatus{s.chat.status(), s.mcp.status()}}
}

// Logs returns recent log lines for a component, or nil if component is unknown.
func (s *Supervisor) Logs(component string, tail int) []string {
	switch component {
	case "chat":
		return s.chat.logs(tail)
	case "mcp":
		return s.mcp.logs(tail)
	default:
		return nil
	}
}

// Wait blocks until supervision goroutines exit.
func (s *Supervisor) Wait() {
	s.wg.Wait()
}

// child represents one supervised process.
type child struct {
	name string
	path string
	args []string
	env  []string

	logBuf *ringBuffer

	mu               sync.Mutex
	pid              int
	restarts         int
	startTime        time.Time
	lastExit         *ExitInfo
	initialBackoff   time.Duration
	maxBackoff       time.Duration
	terminateTimeout time.Duration

	restartCh chan struct{}
}

func newChild(name, path string, args []string, cfg Config) *child {
	return &child{
		name:             name,
		path:             path,
		args:             args,
		logBuf:           newRingBuffer(cfg.BufferLines),
		initialBackoff:   cfg.InitialBackoff,
		maxBackoff:       cfg.MaxBackoff,
		terminateTimeout: cfg.TerminateTimeout,
		restartCh:        make(chan struct{}, 1),
	}
}

func (c *child) run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	backoff := c.initialBackoff
	if backoff <= 0 {
		backoff = time.Second
	}

	for {
		select {
		case <-ctx.Done():
			c.stopRunningProcess(nil)
			return
		case <-c.restartCh:
			// restart requested before start: no-op, continue loop
		default:
		}

		cmd := exec.CommandContext(context.Background(), c.path, c.args...)
		cmd.Env = c.childEnv()
		stdout, _ := cmd.StdoutPipe()
		stderr, _ := cmd.StderrPipe()

		if err := cmd.Start(); err != nil {
			c.recordExit(err, false)
			if !c.sleep(ctx, backoff) {
				return
			}
			backoff = c.nextBackoff(backoff)
			continue
		}

		startedAt := time.Now()
		c.recordStart(cmd.Process.Pid, startedAt)

		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		go c.pipeOutput(stdout, "stdout")
		go c.pipeOutput(stderr, "stderr")

		forcedRestart := false
		var exitErr error
		select {
		case err := <-done:
			exitErr = err
		case <-c.restartCh:
			forcedRestart = true
			exitErr = c.signalAndWait(cmd, done)
		case <-ctx.Done():
			exitErr = c.signalAndWait(cmd, done)
			c.recordExit(exitErr, false)
			return
		}

		c.recordExit(exitErr, true)

		runtime := time.Since(startedAt)
		if runtime > c.maxBackoff {
			backoff = c.initialBackoff
		}

		if forcedRestart {
			backoff = c.initialBackoff
			continue
		}

		if !c.sleep(ctx, backoff) {
			return
		}
		backoff = c.nextBackoff(backoff)
	}
}

func (c *child) signalAndWait(cmd *exec.Cmd, done <-chan error) error {
	if cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}

	select {
	case err := <-done:
		return err
	case <-time.After(c.terminateTimeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return <-done
	}
}

func (c *child) stopRunningProcess(current *exec.Cmd) {
	if current == nil || current.Process == nil {
		return
	}
	_ = current.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(c.terminateTimeout)
	select {
	case <-timer.C:
		_ = current.Process.Kill()
	default:
	}
}

func (c *child) recordStart(pid int, start time.Time) {
	c.mu.Lock()
	c.pid = pid
	c.startTime = start
	c.mu.Unlock()

	c.logBuf.Add(fmt.Sprintf("[%s] started pid=%d", c.name, pid))
}

func (c *child) recordExit(err error, countRestart bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	exitInfo := &ExitInfo{Time: time.Now()}
	if err != nil {
		exitInfo.Error = err.Error()
		if ee, ok := err.(*exec.ExitError); ok {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
				exitInfo.ExitCode = ws.ExitStatus()
			}
		}
	}

	c.lastExit = exitInfo
	c.pid = 0
	if countRestart {
		c.restarts++
	}

	c.logBuf.Add(fmt.Sprintf("[%s] exited: %s", c.name, exitSummary(exitInfo)))
}

func exitSummary(info *ExitInfo) string {
	if info == nil {
		return "unknown"
	}
	if info.Error != "" {
		return info.Error
	}
	if info.ExitCode != 0 {
		return fmt.Sprintf("code=%d", info.ExitCode)
	}
	return "ok"
}

func (c *child) pipeOutput(r io.ReadCloser, stream string) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := fmt.Sprintf("[%s][%s] %s", c.name, stream, scanner.Text())
		c.logBuf.Add(line)
		if forwardChildLogs {
			log.Print(line)
		}
	}
}

func isForwardChildLogsEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("PAYRAM_AGENT_FORWARD_CHILD_LOGS")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func (c *child) childEnv() []string {
	base := os.Environ()
	switch c.name {
	case "chat":
		base = ensureEnv(base, "PAYRAM_CHAT_PORT", "2358")
	case "mcp":
		base = ensureEnv(base, "PAYRAM_MCP_PORT", "3333")
	}
	base = ensureOpenAIKey(base)
	return base
}

func ensureEnv(env []string, key, def string) []string {
	if hasEnv(env, key) {
		return env
	}
	return append(env, fmt.Sprintf("%s=%s", key, def))
}

func ensureOpenAIKey(env []string) []string {
	const prefix = "OPENAI_API_KEY="
	if hasEnvWithValue(env, "OPENAI_API_KEY") {
		return env
	}

	sec, _, err := secrets.Load(update.HomeDir())
	if err != nil || sec.OpenAIAPIKey == "" {
		return env
	}

	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + sec.OpenAIAPIKey
			return env
		}
	}

	return append(env, prefix+sec.OpenAIAPIKey)
}

func hasEnv(env []string, key string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return true
		}
	}
	return false
}

func hasEnvWithValue(env []string, key string) bool {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return len(kv) > len(prefix)
		}
	}
	return false
}

func (c *child) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (c *child) nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > c.maxBackoff {
		return c.maxBackoff
	}
	return next
}

func (c *child) triggerRestart() {
	select {
	case c.restartCh <- struct{}{}:
	default:
	}
}

func (c *child) status() ComponentStatus {
	c.mu.Lock()
	defer c.mu.Unlock()

	return ComponentStatus{
		Name:      c.name,
		PID:       c.pid,
		StartTime: c.startTime,
		Restarts:  c.restarts,
		LastExit:  c.lastExit,
	}
}

func (c *child) logs(tail int) []string {
	return c.logBuf.Tail(tail)
}

func getenvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
