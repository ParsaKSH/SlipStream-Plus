package embedded

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ParsaKSH/SlipStream-Plus/slipstreamorg"
)

// IsEmbedded returns true if the slipstream binary is embedded.
func IsEmbedded() bool {
	return len(slipstreamorg.BinaryData) > 0
}

// ExtractBinary extracts the embedded binary to a temp directory
// and returns the path. The caller should defer cleanup with os.RemoveAll.
func ExtractBinary() (string, func(), error) {
	if !IsEmbedded() {
		return "", func() {}, fmt.Errorf("no embedded binary")
	}

	tmpDir, err := os.MkdirTemp("", "slipstreamplus-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temp dir: %w", err)
	}

	cleanup := func() {
		os.RemoveAll(tmpDir)
	}

	name := "slipstream-client"
	if runtime.GOOS == "windows" {
		name = "slipstream-client.exe"
	}

	binPath := filepath.Join(tmpDir, name)
	if err := os.WriteFile(binPath, slipstreamorg.BinaryData, 0755); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("write binary: %w", err)
	}

	log.Printf("[embedded] extracted slipstream-client to %s (%d bytes)", binPath, len(slipstreamorg.BinaryData))
	return binPath, cleanup, nil
}
