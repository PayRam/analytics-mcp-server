package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Manifest describes an available update.
type Manifest struct {
	Name          string        `json:"name"`
	Channel       string        `json:"channel"`
	Version       string        `json:"version"`
	ReleasedAt    time.Time     `json:"released_at"`
	Notes         string        `json:"notes"`
	Artifacts     Artifacts     `json:"artifacts"`
	Compatibility Compatibility `json:"compatibility"`
	Revoked       bool          `json:"revoked"`
}

// Artifacts contains binaries for each component.
type Artifacts struct {
	Chat Artifact  `json:"chat"`
	MCP  Artifact  `json:"mcp"`
	Bot  *Artifact `json:"bot,omitempty"`
}

// Artifact describes a downloadable binary.
type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Compatibility captures version ranges for dependencies.
type Compatibility struct {
	PayramCore Range `json:"payram_core"`
}

// Range defines min/max versions.
type Range struct {
	Min string `json:"min"`
	Max string `json:"max"`
}

// GetPayramCoreVersion queries the payram-core service for its version.
func GetPayramCoreVersion(ctx context.Context, coreBaseURL string) (string, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	url := strings.TrimRight(coreBaseURL, "/") + "/internal/version"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.Version == "" {
		return "", errors.New("empty version")
	}

	return body.Version, nil
}

// ParseVersion parses "1.2.3" into numeric parts.
func ParseVersion(s string) (int, int, int, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	pat, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, false
	}
	return maj, min, pat, true
}

// CompareVersions compares a and b returning -1,0,1.
func CompareVersions(a, b string) (int, error) {
	amaj, amin, apat, ok := ParseVersion(a)
	if !ok {
		return 0, fmt.Errorf("invalid version %q", a)
	}
	bmaj, bmin, bpat, ok := ParseVersion(b)
	if !ok {
		return 0, fmt.Errorf("invalid version %q", b)
	}

	switch {
	case amaj != bmaj:
		if amaj < bmaj {
			return -1, nil
		}
		return 1, nil
	case amin != bmin:
		if amin < bmin {
			return -1, nil
		}
		return 1, nil
	case apat != bpat:
		if apat < bpat {
			return -1, nil
		}
		return 1, nil
	default:
		return 0, nil
	}
}

// MatchesMax checks if version satisfies a max constraint which may end with ".x".
func MatchesMax(version, max string) (bool, error) {
	if strings.HasSuffix(max, ".x") {
		max = strings.TrimSuffix(max, ".x")
		majMax, minMax, _, ok := ParseVersion(max + ".0")
		if !ok {
			return false, fmt.Errorf("invalid max %q", max)
		}
		maj, min, _, ok := ParseVersion(version)
		if !ok {
			return false, fmt.Errorf("invalid version %q", version)
		}
		if maj < majMax {
			return true, nil
		}
		if maj > majMax {
			return false, nil
		}
		return min <= minMax, nil
	}

	cmp, err := CompareVersions(version, max)
	if err != nil {
		return false, err
	}
	return cmp <= 0, nil
}

// IsCompatible checks coreVersion against min/max, returning a reason when incompatible.
func IsCompatible(coreVersion, min, max string) (bool, string) {
	if min != "" {
		cmp, err := CompareVersions(coreVersion, min)
		if err != nil {
			return false, "invalid core or min version"
		}
		if cmp < 0 {
			return false, fmt.Sprintf("Requires payram-core >= %s", min)
		}
	}

	if max != "" {
		ok, err := MatchesMax(coreVersion, max)
		if err != nil {
			return false, "invalid max version"
		}
		if !ok {
			if strings.HasSuffix(max, ".x") {
				return false, fmt.Sprintf("Requires payram-core %s", max)
			}
			return false, fmt.Sprintf("Requires payram-core <= %s", max)
		}
	}

	return true, ""
}

// FetchManifest downloads manifest and signature for a channel.
func FetchManifest(ctx context.Context, baseURL, channel string) (Manifest, []byte, []byte, error) {
	var manifest Manifest
	base := strings.TrimRight(baseURL, "/")
	if channel == "" {
		channel = "stable"
	}
	manifestURL := fmt.Sprintf("%s/%s/manifest.json", base, channel)
	sigURL := manifestURL + ".sig"

	raw, err := fetchBytes(ctx, manifestURL)
	if err != nil {
		return manifest, nil, nil, err
	}

	sig, err := fetchBytes(ctx, sigURL)
	if err != nil {
		return manifest, raw, nil, err
	}

	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, raw, sig, err
	}

	return manifest, raw, sig, nil
}

// VerifyManifest checks a manifest signature using a base64 public key.
func VerifyManifest(raw, sig []byte, pubKeyB64 string) error {
	pub, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil {
		return fmt.Errorf("invalid public key: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("invalid public key length")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), raw, sig) {
		return errors.New("signature verification failed")
	}
	return nil
}

// DownloadToFile streams a URL to a destination file.
func DownloadToFile(ctx context.Context, url, dstPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}

	tmp := dstPath + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}

	if err := os.Rename(tmp, dstPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	return nil
}

// VerifySHA256 checks a file hash against expected hex.
func VerifySHA256(filePath, expectedHex string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	sum := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(sum, expectedHex) {
		return fmt.Errorf("sha256 mismatch: got %s expected %s", sum, expectedHex)
	}
	return nil
}

func fetchBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}
