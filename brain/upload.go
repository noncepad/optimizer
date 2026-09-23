package brain

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	mgrbot "git.noncepad.com/pkg/bot/catscope"
	"git.noncepad.com/pkg/solpipe-util/logger"
	sgo "github.com/gagliardetto/solana-go"
)

// UploadRequest describes one bot image to upload and run. Submit via
// Hook.Request; the result (a *Bot, or an error) arrives on ResultC.
type UploadRequest struct {
	// Mode is the MODE env var the WASM bot image dispatches on at
	// startup (e.g. "arbv1", "testperpv1", "testperplatencyv1lite") --
	// see catscope-rust-bot's brain::mod BotMode::from_env.
	Mode string
	// BotImagePath is a local path to compile from; empty downloads the
	// published default image instead (see downloadDefaultImage).
	BotImagePath string
	// Env is merged with {"MODE": Mode} and forwarded to the image at
	// load/compile time (e.g. TEST_PROTOCOL) -- may be nil.
	Env map[string]string
	// Pipeline selects which validator to upload to; the zero value
	// uses the pipeline Init allocated (see eventHook.defaultPipeline).
	Pipeline sgo.PublicKey
	// Timeout bounds the whole allocate-a-connection/upload/handshake
	// retry loop; <= 0 uses defaultUploadTimeout.
	Timeout time.Duration
	// ResultC receives exactly one *UploadResult. Request allocates
	// this (buffered by 1) if left nil.
	ResultC chan *UploadResult
}

// NewUploadRequest builds a request with ResultC already allocated --
// the common case (Env/Pipeline/Timeout left at their zero values).
func NewUploadRequest(mode string) *UploadRequest {
	return &UploadRequest{Mode: mode, ResultC: make(chan *UploadResult, 1)}
}

// UploadResult is delivered on an UploadRequest's ResultC exactly once.
// Exactly one of Bot/Err is set.
type UploadResult struct {
	Bot *Bot
	Err error
}

// Request submits req to the background dispatcher Init started. See
// UploadRequest's own doc comment for what happens to it next.
func (hs *eventHook) Request(req *UploadRequest) {
	if req.ResultC == nil {
		req.ResultC = make(chan *UploadResult, 1)
	}
	select {
	case hs.uploadRequestC <- req:
	case <-hs.ctx.Done():
		req.ResultC <- &UploadResult{Err: hs.ctx.Err()}
	}
}

// Bots returns a snapshot of every currently-tracked bot, keyed by the
// pipeline it's running on.
func (hs *eventHook) Bots() map[sgo.PublicKey]*Bot {
	hs.mx.Lock()
	defer hs.mx.Unlock()
	out := make(map[sgo.PublicKey]*Bot, len(hs.mBot))
	for k, v := range hs.mBot {
		out[k] = v
	}
	return out
}

// dispatchUploads drains uploadRequestC one request at a time -- serial,
// not concurrent, because botImage.Upload's own doc comment notes it
// kills whatever instance is already running on a pipeline; two uploads
// racing (even against different pipelines, since they share this
// eventHook's builder/addressBook) is a real footgun this avoids by
// construction rather than by asking every caller to coordinate.
func (hs *eventHook) dispatchUploads() {
	doneC := hs.ctx.Done()
	for {
		select {
		case <-doneC:
			return
		case req := <-hs.uploadRequestC:
			bot, err := hs.upload(req)
			if err == nil {
				hs.mx.Lock()
				hs.mBot[bot.Pipeline()] = bot
				hs.mx.Unlock()
			}
			req.ResultC <- &UploadResult{Bot: bot, Err: err}
		}
	}
}

// defaultUploadTimeout bounds the whole allocate-a-connection/upload/
// handshake retry loop for a request that doesn't set its own Timeout --
// matches every existing brain/* bot mode's own hardcoded 5-minute
// budget for this exact loop.
const defaultUploadTimeout = 5 * time.Minute

// uploadRetryInterval is how long upload waits between retrying
// botImage.Upload after a failed attempt -- matches every existing
// brain/* bot mode's own hardcoded 30s.
const uploadRetryInterval = 30 * time.Second

// upload does the real work: load the image, open its log file, then
// retry botImage.Upload + wait-for-handshake until timeout. This is the
// same retry loop every existing brain/* bot mode's own Init used to
// duplicate inline -- generalized here to take an arbitrary
// mode/image/pipeline/timeout instead of one hardcoded at compile time.
func (hs *eventHook) upload(req *UploadRequest) (*Bot, error) {
	entry := hs.logger.With("mode", req.Mode)
	pipeline := req.Pipeline
	if pipeline.IsZero() {
		pipeline = hs.defaultPipeline
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultUploadTimeout
	}

	mEnvLoad := make(map[string]string, len(req.Env)+1)
	for k, v := range req.Env {
		mEnvLoad[k] = v
	}
	mEnvLoad["MODE"] = req.Mode

	var botImage mgrbot.Image
	var err error
	if 0 < len(req.BotImagePath) {
		botImage, err = hs.useLocalImage(hs.ctx, req.BotImagePath, mEnvLoad)
	} else {
		botImage, err = hs.downloadDefaultImage(hs.ctx, mEnvLoad)
	}
	if err != nil {
		return nil, err
	}

	logPath, logFileRaw, err := hs.createLogFile(req.Mode, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to create log file: %s", err)
	}
	// Wrapped so Close is idempotent -- see onceCloser's own doc comment.
	// Real, live-hit reason: a failed handshake can retry, spawning
	// another instance.LogToFile goroutine on this same file below, and
	// each one closes it when its own stream ends; Bot.Close (bot.go)
	// closes it too. Without this, whichever of those runs first turns
	// every one after it into a double-close.
	logFile := newOnceCloser(logFileRaw)

	doneC := hs.ctx.Done()
	timeStart := time.Now()
	timeFinish := timeStart.Add(timeout)
	var instance mgrbot.Bot
	var handshake *mgrbot.Handshake
uploadloop:
	for timeFinish.After(time.Now()) {
		mEnvRun := map[string]string{"RUST_BACKTRACE": "1"}
		// if there is already an instance running on pipeline, it will
		// be killed and replaced by this one.
		instance, err = botImage.Upload(pipeline, nil, mEnvRun)
		if err != nil {
			select {
			case <-doneC:
				break uploadloop
			case <-time.After(uploadRetryInterval):
				entry.With(logger.Loc("upload", 1)).Info("waiting for upload")
				continue
			}
		}
		go instance.LogToFile(hs.ctx, logFile, true)
		handshakeC := instance.OnHandshake()
		err = nil
		select {
		case <-doneC:
			err = hs.ctx.Err()
		case <-time.After(time.Until(timeFinish)):
			err = errors.New("timed out waiting for handshake")
		case x := <-handshakeC:
			err = x.Error
			if err == nil {
				handshake = &x
				break uploadloop
			}
		}
		if err != nil {
			entry.With(logger.Loc("upload", 2), "err", err).Info("failed to get bot client for pipeline")
		}
	}
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("failed to get bot client for pipeline %s: %s", pipeline, err)
	}
	if handshake == nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("failed to get bot client for pipeline %s: timed out", pipeline)
	}
	if handshake.Error != nil {
		_ = logFile.Close()
		return nil, handshake.Error
	}
	entry.With(logger.Loc("upload", 3)).Info(fmt.Sprintf("bot uploaded and handshake complete: %s", handshake))
	return newBot(hs.ctx, req.Mode, pipeline, *handshake, instance, logPath, logFile, entry), nil
}

// createLogFile opens a fresh, real on-disk file for one bot instance's
// stderr -- see Configuration.LogDir's own doc comment. Name includes a
// nanosecond timestamp so re-uploading the same mode to the same
// pipeline (e.g. after a crash) never clobbers the previous attempt's
// log.
func (hs *eventHook) createLogFile(mode string, pipeline sgo.PublicKey) (string, *os.File, error) {
	dir := hs.config.LogDir
	if len(dir) == 0 {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", nil, err
	}
	name := fmt.Sprintf("%s-%s-%d.log", mode, pipeline, time.Now().UnixNano())
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", nil, err
	}
	return path, f, nil
}
