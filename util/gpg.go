package util

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"golang.org/x/crypto/openpgp"           //nolint:staticcheck
	"golang.org/x/crypto/openpgp/clearsign" //nolint:staticcheck
)

// DownloadCatscopeRustBotDemonstrator downloads a Noncepad build demonstration web assembly blob
func DownloadCatscopeRustBotDemonstrator(ctx context.Context) (string, error) {
	return downloadFile(ctx, "https://noncepad.com/dev/catscope_rust_bot.wasm")
}

func downloadFile(ctx context.Context, url string) (string, error) {
	f, err := os.CreateTemp("/tmp", "catscoperustbot*.wasm")
	if err != nil {
		return "", fmt.Errorf("failed to create file: %s", err)
	}
	filePath := f.Name()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		_ = f.Close()
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_ = f.Close()
		return "", fmt.Errorf("fetch: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		_ = f.Close()
		_ = resp.Body.Close()
		return "", fmt.Errorf("fetch: unexpected status %s", resp.Status)
	}
	_, err = io.Copy(f, resp.Body)
	_ = f.Close()
	_ = resp.Body.Close()
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	return filePath, nil
}

// DownloadSigned fetches a PGP clear-signed file from url, verifies its
// signature against armoredKeyring, and returns the decoded binary payload.
// The signed message body must be the binary data encoded as standard base64.
func DownloadSigned(ctx context.Context, url string, armoredKeyring io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch: unexpected status %s", resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	block, _ := clearsign.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("not a valid PGP clear-signed message")
	}
	if block.ArmoredSignature == nil {
		return nil, fmt.Errorf("clear-signed message missing signature block")
	}

	keyring, err := openpgp.ReadArmoredKeyRing(armoredKeyring)
	if err != nil {
		return nil, fmt.Errorf("parse keyring: %w", err)
	}
	_, err = openpgp.CheckDetachedSignature(keyring, bytes.NewReader(block.Bytes), block.ArmoredSignature.Body)
	if err != nil {
		return nil, fmt.Errorf("signature invalid: %w", err)
	}

	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(block.Plaintext)))
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	return data, nil
}

// VerifyDetached checks a detached PGP signature (armored .asc or raw binary
// .sig) against signed and returns an error if verification fails.
func VerifyDetached(signed io.Reader, sig io.Reader, armoredKeyring io.Reader) error {
	keyring, err := openpgp.ReadArmoredKeyRing(armoredKeyring)
	if err != nil {
		return fmt.Errorf("parse keyring: %w", err)
	}

	// Buffer the signature so we can attempt armored then binary parsing.
	sigBytes, err := io.ReadAll(sig)
	if err != nil {
		return fmt.Errorf("read signature: %w", err)
	}

	_, err = openpgp.CheckArmoredDetachedSignature(keyring, signed, bytes.NewReader(sigBytes))
	if err == nil {
		return nil
	}

	// Reset signed reader — requires it to be a *bytes.Reader or similar.
	// Re-read signed content for binary sig attempt.
	if rs, ok := signed.(io.ReadSeeker); ok {
		if _, seekErr := rs.Seek(0, io.SeekStart); seekErr != nil {
			return fmt.Errorf("signature invalid: %w", err)
		}
		_, binErr := openpgp.CheckDetachedSignature(keyring, rs, bytes.NewReader(sigBytes))
		if binErr == nil {
			return nil
		}
	}

	return fmt.Errorf("signature invalid: %w", err)
}
