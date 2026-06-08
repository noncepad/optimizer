package util

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	sgo "github.com/gagliardetto/solana-go"
)

var botPipelineWhiteList = ""

func BotPipelineWhiteList() []sgo.PublicKey {
	list := strings.Split(",", botPipelineWhiteList)
	ans := make([]sgo.PublicKey, len(list))
	var err error
	for i, x := range list {
		ans[i], err = sgo.PublicKeyFromBase58(x)
		if err != nil {
			panic(fmt.Errorf("failed to parse public key from %s: %s", x, err))
		}
	}
	return ans
}

const wasmURLBouncerV1 = "https://noncepad.com/dev/catscope_rust_bot.wasm"

func downloadWasmBouncerV1(ctx context.Context) (string, error) {
	return downloadWasm(ctx, wasmURLBouncerV1)
}

func downloadWasm(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download wasm: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download wasm: unexpected status %s", resp.Status)
	}
	f, err := os.CreateTemp("", "target.wasm")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	_ = f.Close()
	if _, err = io.Copy(f, resp.Body); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write wasm: %w", err)
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("seek wasm: %w", err)
	}
	return f.Name(), nil
}
