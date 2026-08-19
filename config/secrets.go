package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadPrivateKey attempts to load the DK private key from multiple sources in order of preference.
// Priority order:
// 1. File path (DKPG_PRIVATE_KEY_FILE) - Best for local/UAT, allows strict file permissions
// 2. Base64 env var (DKPG_PRIVATE_KEY_B64) - Current approach, backward compatible
// 3. Plain text env var (DKPG_PRIVATE_KEY) - Legacy support
// 4. Empty string - Will trigger API fetch from DK Bank in dkpg client
//
// Why needed: Provides flexible, secure private key loading with smooth upgrade path.
// Called from: main() during application initialization.
func LoadPrivateKey() (string, error) {
	// Option 1: File-based storage (RECOMMENDED)
	// Allows chmod 600 permissions, easier rotation, not in process env
	if keyPath := os.Getenv("DKPG_PRIVATE_KEY_FILE"); keyPath != "" {
		resolvedPath, warnings, err := validatePrivateKeyPathPolicy(keyPath)
		for _, warning := range warnings {
			_, _ = fmt.Fprintf(os.Stderr, "WARNING: %s\n", warning)
		}
		if err != nil {
			return "", err
		}

		// #nosec G304,G703 -- resolvedPath is normalized and checked by validatePrivateKeyPathPolicy before read.
		data, err := os.ReadFile(resolvedPath)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "private key file read failed path=%q err=%v\n", resolvedPath, err)
			return "", fmt.Errorf("failed to read private key from configured file")
		}

		keyContent := strings.TrimSpace(string(data))
		if keyContent == "" {
			return "", fmt.Errorf("configured private key file is empty")
		}

		// Validate it looks like a PEM key
		if !strings.Contains(keyContent, "BEGIN") || !strings.Contains(keyContent, "PRIVATE KEY") {
			return "", fmt.Errorf("configured private key file does not appear to contain a valid PEM key")
		}

		return keyContent, nil
	}

	// Option 2: Base64 encoded environment variable
	// Current approach, maintains backward compatibility
	if keyB64 := os.Getenv("DKPG_PRIVATE_KEY_B64"); keyB64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(keyB64)
		if err != nil {
			return "", fmt.Errorf("failed to decode DKPG_PRIVATE_KEY_B64: %w", err)
		}

		keyContent := strings.TrimSpace(string(decoded))
		if keyContent == "" {
			return "", fmt.Errorf("decoded DKPG_PRIVATE_KEY_B64 is empty")
		}

		return keyContent, nil
	}

	// Option 3: Plain text environment variable (legacy)
	// Supports \n escape sequences in the value
	if key := os.Getenv("DKPG_PRIVATE_KEY"); key != "" {
		// Replace literal \n with actual newlines
		keyContent := strings.ReplaceAll(key, "\\n", "\n")
		keyContent = strings.TrimSpace(keyContent)

		if keyContent == "" {
			return "", fmt.Errorf("DKPG_PRIVATE_KEY is empty")
		}

		return keyContent, nil
	}

	// Option 4: No key provided - will fetch from DK Bank API
	// The dkpg.Client will handle this fallback automatically
	return "", nil
}

// ValidatePrivateKeyFile checks if a private key file exists and is readable.
// Why needed: Helps with startup validation and clear error messages.
// Called from: main() or health check endpoints.
func ValidatePrivateKeyFile(path string) error {
	if path == "" {
		return nil // No file path configured, will use other methods
	}

	resolvedPath, warnings, err := validatePrivateKeyPathPolicy(path)
	for _, warning := range warnings {
		_, _ = fmt.Fprintf(os.Stderr, "WARNING: %s\n", warning)
	}
	if err != nil {
		return err
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("private key file does not exist")
		}
		_, _ = fmt.Fprintf(os.Stderr, "private key file stat failed path=%q err=%v\n", resolvedPath, err)
		return fmt.Errorf("cannot access private key file")
	}

	if info.IsDir() {
		return fmt.Errorf("private key path is a directory, not a file")
	}

	// Check file permissions (warn if too permissive on Unix systems)
	mode := info.Mode()
	if mode.Perm()&0077 != 0 {
		// File is readable/writable by group or others
		fmt.Printf("WARNING: Private key file %s has permissive permissions (%s). Recommend: chmod 600 %s\n",
			resolvedPath, mode.Perm().String(), resolvedPath)
	}

	return nil
}

func validatePrivateKeyPathPolicy(path string) (string, []string, error) {
	var warnings []string

	raw := strings.TrimSpace(path)
	if raw == "" {
		return "", warnings, fmt.Errorf("private key file path is empty")
	}
	if strings.Contains(raw, "\x00") {
		return "", warnings, fmt.Errorf("private key file path is invalid")
	}

	mode := secretFilePathMode()
	if strings.Contains(raw, "..") {
		msg := "private key file path contains '..'; this is discouraged and may be blocked in enforce mode"
		if mode == "enforce" {
			return "", warnings, fmt.Errorf("private key file path is not allowed by policy")
		}
		warnings = append(warnings, msg)
	}

	cleaned := filepath.Clean(raw)
	absPath, err := filepath.Abs(cleaned)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "private key path resolve failed path=%q err=%v\n", raw, err)
		return "", warnings, fmt.Errorf("failed to resolve private key file path")
	}
	resolvedPath := absPath
	if evalPath, err := filepath.EvalSymlinks(absPath); err == nil {
		resolvedPath = evalPath
	} else if !errors.Is(err, os.ErrNotExist) {
		warnings = append(warnings, "unable to fully resolve private key file symlinks")
	}

	if baseDir := strings.TrimSpace(os.Getenv("SECRET_FILE_BASE_DIR")); baseDir != "" {
		baseAbs, err := filepath.Abs(filepath.Clean(baseDir))
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "secret base dir resolve failed path=%q err=%v\n", baseDir, err)
			return "", warnings, fmt.Errorf("invalid secret file base directory")
		}
		if evalBase, err := filepath.EvalSymlinks(baseAbs); err == nil {
			baseAbs = evalBase
		} else if !errors.Is(err, os.ErrNotExist) {
			warnings = append(warnings, "unable to fully resolve secret file base directory symlinks")
		}

		if !pathWithinBaseDir(baseAbs, resolvedPath) {
			msg := "private key file is outside SECRET_FILE_BASE_DIR"
			if mode == "enforce" {
				return "", warnings, fmt.Errorf("private key file path is not allowed by policy")
			}
			warnings = append(warnings, msg)
		}
	}

	return resolvedPath, warnings, nil
}

func secretFilePathMode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("SECRET_FILE_PATH_MODE")))
	if mode == "enforce" {
		return "enforce"
	}
	return "warn"
}

func pathWithinBaseDir(baseDir, candidate string) bool {
	baseDir = filepath.Clean(strings.TrimSpace(baseDir))
	candidate = filepath.Clean(strings.TrimSpace(candidate))
	if baseDir == "" || candidate == "" {
		return false
	}
	rel, err := filepath.Rel(baseDir, candidate)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}
