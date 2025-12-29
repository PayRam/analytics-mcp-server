package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/payram/payram-analytics-mcp-server/internal/agent/update"
)

// Options captures manifest generation settings.
type Options struct {
	Name       string
	Channel    string
	Version    string
	Notes      string
	ReleasedAt time.Time
	ChatURL    string
	ChatSHA    string
	MCPURL     string
	MCPSHA     string
	BotURL     string
	BotSHA     string
	CoreMin    string
	CoreMax    string
	OutputDir  string
	PrivKeyB64 string
}

func main() {
	opts, err := parseFlags()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	raw, sig, pubB64, err := Generate(*opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("manifest written to %s\n", filepath.Join(opts.OutputDir, "manifest.json"))
	fmt.Printf("signature written to %s\n", filepath.Join(opts.OutputDir, "manifest.json.sig"))
	fmt.Printf("public key (base64): %s\n", pubB64)
	fmt.Printf("manifest sha256 bytes signed: %d\n", len(raw))
	_ = sig
}

func parseFlags() (*Options, error) {
	var (
		channel    = flag.String("channel", "stable", "channel to update (stable|beta)")
		version    = flag.String("version", "", "version string (vX.Y.Z or X.Y.Z)")
		notes      = flag.String("notes", "", "release notes")
		releasedAt = flag.String("released_at", "", "RFC3339 timestamp (default: now UTC)")
		chatURL    = flag.String("chat_url", "", "chat artifact URL")
		chatSHA    = flag.String("chat_sha", "", "chat artifact sha256 (hex)")
		mcpURL     = flag.String("mcp_url", "", "mcp artifact URL")
		mcpSHA     = flag.String("mcp_sha", "", "mcp artifact sha256 (hex)")
		botURL     = flag.String("bot_url", "", "bot artifact URL (optional)")
		botSHA     = flag.String("bot_sha", "", "bot artifact sha256 (hex, optional if bot_url empty)")
		coreMin    = flag.String("core_min", "", "payram-core minimum version")
		coreMax    = flag.String("core_max", "", "payram-core maximum version")
		name       = flag.String("name", "payram-analytics", "manifest name")
		outDir     = flag.String("output_dir", ".", "output directory for manifest files")
		privB64    = flag.String("privkey_b64", "", "ed25519 private key (base64, 64 bytes)")
	)

	flag.Parse()

	if *version == "" {
		return nil, errors.New("version is required")
	}
	if *chatURL == "" || *chatSHA == "" {
		return nil, errors.New("chat_url and chat_sha are required")
	}
	if *mcpURL == "" || *mcpSHA == "" {
		return nil, errors.New("mcp_url and mcp_sha are required")
	}

	ts := *releasedAt
	if ts == "" {
		ts = time.Now().UTC().Format(time.RFC3339)
	}
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return nil, fmt.Errorf("invalid released_at: %w", err)
	}

	priv := *privB64
	if priv == "" {
		priv = os.Getenv("PAYRAM_UPDATE_ED25519_PRIVKEY_B64")
	}
	if priv == "" {
		return nil, errors.New("privkey_b64 or PAYRAM_UPDATE_ED25519_PRIVKEY_B64 is required")
	}

	return &Options{
		Name:       *name,
		Channel:    *channel,
		Version:    trimVersionPrefix(*version),
		Notes:      *notes,
		ReleasedAt: parsed,
		ChatURL:    *chatURL,
		ChatSHA:    strings.ToLower(*chatSHA),
		MCPURL:     *mcpURL,
		MCPSHA:     strings.ToLower(*mcpSHA),
		BotURL:     *botURL,
		BotSHA:     strings.ToLower(*botSHA),
		CoreMin:    *coreMin,
		CoreMax:    *coreMax,
		OutputDir:  *outDir,
		PrivKeyB64: priv,
	}, nil
}

// Generate creates manifest.json and manifest.json.sig based on options.
// It returns the raw manifest bytes (as written) and the signature.
func Generate(opts Options) ([]byte, []byte, string, error) {
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return nil, nil, "", err
	}

	priv, err := base64.StdEncoding.DecodeString(opts.PrivKeyB64)
	if err != nil {
		return nil, nil, "", fmt.Errorf("decode privkey: %w", err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, nil, "", fmt.Errorf("invalid private key length: %d", len(priv))
	}
	privKey := ed25519.PrivateKey(priv)
	pubKey := privKey.Public().(ed25519.PublicKey)
	pubB64 := base64.StdEncoding.EncodeToString(pubKey)

	manifest := update.Manifest{
		Name:       opts.Name,
		Channel:    normalizeChannel(opts.Channel),
		Version:    trimVersionPrefix(opts.Version),
		ReleasedAt: opts.ReleasedAt.UTC(),
		Notes:      opts.Notes,
		Artifacts: update.Artifacts{
			Chat: update.Artifact{URL: opts.ChatURL, SHA256: opts.ChatSHA},
			MCP:  update.Artifact{URL: opts.MCPURL, SHA256: opts.MCPSHA},
		},
		Compatibility: update.Compatibility{PayramCore: update.Range{Min: opts.CoreMin, Max: opts.CoreMax}},
		Revoked:       false,
	}

	if strings.TrimSpace(opts.BotURL) != "" {
		manifest.Artifacts.Bot = &update.Artifact{URL: opts.BotURL, SHA256: opts.BotSHA}
	}

	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, nil, "", err
	}
	raw = append(raw, '\n')

	sig := ed25519.Sign(privKey, raw)

	manifestPath := filepath.Join(opts.OutputDir, "manifest.json")
	sigPath := manifestPath + ".sig"

	if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
		return nil, nil, "", err
	}
	if err := os.WriteFile(sigPath, sig, 0o644); err != nil {
		return nil, nil, "", err
	}

	return raw, sig, pubB64, nil
}

func normalizeChannel(ch string) string {
	ch = strings.ToLower(strings.TrimSpace(ch))
	if ch == "" {
		return "stable"
	}
	switch ch {
	case "stable", "beta":
		return ch
	default:
		return ch
	}
}

func trimVersionPrefix(v string) string {
	return strings.TrimPrefix(v, "v")
}
