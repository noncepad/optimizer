package prefetch

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"git.noncepad.com/pkg/optimizer/prefetch/trading"
)

type StaticLoader interface {
	// modify arguments to cargo build
	Args([]string) ([]string, error)
	// put in files that can be statically compiled into the wasm bot
	Load(string, *trading.TradingPair) error
	// Env set environmental variables
	Env(map[string]string) error
}

// Build an image
func (pf *Prefetcher) Build(ctx context.Context, targetRepositoryPath string, listLoader []StaticLoader) (*BotImage, error) {
	tmpdir, err := os.MkdirTemp(pf.tmpdir, "prefactor*")
	if err != nil {
		return nil, fmt.Errorf("failed to create logging directory: %s", err)
	}
	botF, err := os.CreateTemp(pf.tmpdir, "bot*")
	if err != nil {
		return nil, fmt.Errorf("failed to create bot blob: %s", err)
	}

	stdoutLog, err := os.Create(filepath.Join(tmpdir, "stdout.log"))
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %s", err)
	}
	stderrLog, err := os.Create(filepath.Join(tmpdir, "stderr.log"))
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %s", err)
	}
	_ = os.Mkdir(filepath.Join(targetRepositoryPath, "target"), 0o755)
	args := []string{
		"build", "--target", "wasm32-wasip2", "--release",
	}
	for _, loader := range listLoader {
		args, err = loader.Args(args)
		if err != nil {
			return nil, fmt.Errorf("failed to load args: %s", err)
		}
	}
	for _, loader := range listLoader {
		// create target directory
		err = loader.Load(filepath.Join(targetRepositoryPath, "target"), pf.trading)
		if err != nil {
			return nil, fmt.Errorf("failed to load static files: %s", err)
		}
	}
	mEnv := make(map[string]string)
	for _, v := range os.Environ() {
		y := strings.Split(v, "=")
		if len(y) < 2 {
			return nil, fmt.Errorf("failed to split %s; %d %d", v, len(y), 2)
		}
		mEnv[y[0]] = strings.Join(y[1:], "=")
	}
	for _, loader := range listLoader {
		err = loader.Env(mEnv)
		if err != nil {
			return nil, fmt.Errorf("env load failed: %s", err)
		}
	}
	err = pf.trading.Export(filepath.Join(targetRepositoryPath, "target", "trading.json"))
	if err != nil {
		return nil, fmt.Errorf("failed to write trading.json: %s", err)
	}
	// create the command
	cmd := exec.CommandContext(ctx, "cargo", args...)
	cmd.Env = make([]string, len(mEnv))
	{
		i := 0
		for k, v := range mEnv {
			cmd.Env[i] = fmt.Sprintf("%s=%s", k, v)
			i++
		}
	}
	cmd.Dir = targetRepositoryPath
	cmd.Stdin = nil
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to pipe: %s", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to pipe: %s", err)
	}
	entry := pf.logger
	go func() {
		_, err2 := io.Copy(stdoutLog, stdout)
		if err2 != nil {
			entry.Error(err2.Error())
		}
	}()
	go func() {
		_, err2 := io.Copy(stdoutLog, stdout)
		_ = stdout.Close()
		_ = stdoutLog.Close()
		if err2 != nil {
			entry.Error(err2.Error())
		}
	}()
	go func() {
		_, err2 := io.Copy(stderrLog, stderr)
		_ = stderr.Close()
		_ = stderrLog.Close()
		if err2 != nil {
			entry.Error(err2.Error())
		}
	}()
	//	pf.logger.Warn(fmt.Sprintf("have mEnv %+v", mEnv))
	if len(mEnv) == 0 {
		return nil, fmt.Errorf("empty mEnv %+v", mEnv)
	}
	pf.logger.Info(fmt.Sprintf("compiling wasm bot at %s", targetRepositoryPath))
	err = cmd.Run()
	if err != nil {
		lastErr := err
		f, err := os.Open(filepath.Join(tmpdir, "stderr.log"))
		if err == nil {
			_, _ = io.Copy(os.Stderr, f)
			_ = f.Close()
		}
		return nil, fmt.Errorf("program failed: %s", lastErr)
	}
	_ = os.RemoveAll(tmpdir)
	var botFilePath string
	{
		outF, err := os.Open(filepath.Join(targetRepositoryPath, "target", "wasm32-wasip2", "release", "catscope_rust_bot.wasm"))
		if err != nil {
			return nil, fmt.Errorf("failed to copy blob: %s", err)
		}
		_, err = io.Copy(botF, outF)
		botFilePath = botF.Name()
		_ = botF.Close()
		_ = outF.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to copy blob: %s", err)
		}

	}
	bi := &BotImage{path: botFilePath}
	//	runtime.AddCleanup(bi, func(fp string) {
	//		_ = os.RemoveAll(fp)
	//	}, botFilePath)
	return bi, nil
}

type BotImage struct {
	path string
}

func (bi *BotImage) Path() string {
	return bi.path
}
