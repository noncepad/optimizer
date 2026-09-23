package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"

	mothership "git.noncepad.com/pkg/bot/solpipe/bidder/manager"
	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	brain "git.noncepad.com/pkg/optimizer/brain"
	"git.noncepad.com/pkg/solpipe-util/common"
	sgo "github.com/gagliardetto/solana-go"
)

// CompareCmd uploads testperplatencyv1 to two different validator
// pipelines via brain.Hook.Request and compares the native-transfer
// write-delay (send -> FirstShredReceived) each one reports -- the
// real, per-validator latency number testperplatencyv1 exists to
// measure (see catscope-rust-bot's testperplatencyv1::state's
// report_native_stats).
//
// Real limitation this command works around rather than pretends
// doesn't exist: that number is only ever logged (report_native_stats
// uses log_warn! exclusively) -- there's no structured stdout message
// for it, so it can't be read off Bot.RecvC the way a real wire-protocol
// message could. This reads it back out of each bot's own real, on-disk
// stderr log file instead (Bot.LogPath()), which is exactly what that
// file is for.
type CompareCmd struct {
	ParentKey string `arg:"fee-payer" help:"the file path to the fee payer (parent wallet)"`
	PipelineA string `arg:"pipeline-a" help:"first validator pipeline pubkey to compare"`
	PipelineB string `arg:"pipeline-b" help:"second validator pipeline pubkey to compare"`
	LogDir    string `option:"log-dir" help:"where each bot's stderr log file is written (default: os.TempDir())"`
	// NATIVE_TRANSFER_TARGET (20 real cycles, see testperplatencyv1's own
	// state.rs) plus real validator/network variance is the actual
	// budget here -- this session's own live runs of the same test took
	// anywhere from ~50s to a few minutes, so this errs wide.
	Timeout time.Duration `option:"timeout" default:"10m" help:"how long to wait for each bot to finish its native-transfer-latency run and report stats."`
}

// nativeWriteDelayRe matches report_native_stats's write-delay summary
// line exactly -- see catscope-rust-bot's testperplatencyv1::state.rs.
var nativeWriteDelayRe = regexp.MustCompile(
	`native transfer report -- write delay \(send->FirstShredReceived\): n=(\d+) p50=(\d+)µs p99=(\d+)µs`,
)

type nativeStats struct {
	n, p50Us, p99Us uint64
}

func (r *CompareCmd) Run(rc *RunConfig) error {
	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(r.ParentKey)
	if err != nil {
		return fmt.Errorf("failed to load fee payer: %s", err)
	}
	pipelineA, err := sgo.PublicKeyFromBase58(r.PipelineA)
	if err != nil {
		return fmt.Errorf("bad pipeline-a pubkey: %s", err)
	}
	pipelineB, err := sgo.PublicKeyFromBase58(r.PipelineB)
	if err != nil {
		return fmt.Errorf("bad pipeline-b pubkey: %s", err)
	}

	ctx := rc.Ctx
	cancel := rc.Cancel
	defer cancel(nil)

	dialer, err := bidder.CreateDialer(ctx, parentKey)
	if err != nil {
		return fmt.Errorf("failed to create dialer: %s", err)
	}

	hook := brain.Create(ctx, cancel, parentKey, &brain.Configuration{LogDir: r.LogDir})
	if _, err := mothership.Create(ctx, dialer, hook); err != nil {
		return fmt.Errorf("failed to create mothership: %s", err)
	}

	// TEST_PROTOCOL=native scopes testperplatencyv1 to the real native
	// SOL transfer latency test (as opposed to the Solend/Kamino/
	// Marginfi deposit/withdraw cycle test the same bot mode also
	// supports) -- see TestProtocol::from_env on the Rust side.
	env := map[string]string{"TEST_PROTOCOL": "native"}
	reqA := &brain.UploadRequest{Mode: "testperplatencyv1", Env: env, Pipeline: pipelineA, ResultC: make(chan *brain.UploadResult, 1)}
	reqB := &brain.UploadRequest{Mode: "testperplatencyv1", Env: env, Pipeline: pipelineB, ResultC: make(chan *brain.UploadResult, 1)}
	// Both submitted up front -- Hook's own dispatcher processes uploads
	// one at a time (see brain/upload.go's own doc comment on why), so B
	// simply starts a little after A rather than blocking here.
	hook.Request(reqA)
	hook.Request(reqB)

	fmt.Printf("uploading testperplatencyv1 to pipeline A (%s)...\n", pipelineA)
	resA := <-reqA.ResultC
	if resA.Err != nil {
		return fmt.Errorf("upload to pipeline A (%s) failed: %s", pipelineA, resA.Err)
	}
	botA := resA.Bot
	defer func() { _ = botA.Close() }()
	childA := giveBotUniqueWallet(botA, parentKey, 0)
	fmt.Printf("pipeline A uploaded -- bot=%s child wallet=%s log=%s\n", botA.Wallet(), childA, botA.LogPath())

	fmt.Printf("uploading testperplatencyv1 to pipeline B (%s)...\n", pipelineB)
	resB := <-reqB.ResultC
	if resB.Err != nil {
		return fmt.Errorf("upload to pipeline B (%s) failed: %s", pipelineB, resB.Err)
	}
	botB := resB.Bot
	defer func() { _ = botB.Close() }()
	childB := giveBotUniqueWallet(botB, parentKey, 1)
	fmt.Printf("pipeline B uploaded -- bot=%s child wallet=%s log=%s\n", botB.Wallet(), childB, botB.LogPath())

	deadline := time.Now().Add(r.Timeout)
	fmt.Printf("waiting up to %s for each pipeline's native-transfer-latency report...\n", r.Timeout)

	statsA, err := waitForNativeStats(ctx, botA.LogPath(), deadline)
	if err != nil {
		return fmt.Errorf("pipeline A (%s) never reported native-transfer stats: %s", pipelineA, err)
	}
	fmt.Printf("pipeline A report: n=%d p50=%dµs p99=%dµs\n", statsA.n, statsA.p50Us, statsA.p99Us)

	statsB, err := waitForNativeStats(ctx, botB.LogPath(), deadline)
	if err != nil {
		return fmt.Errorf("pipeline B (%s) never reported native-transfer stats: %s", pipelineB, err)
	}
	fmt.Printf("pipeline B report: n=%d p50=%dµs p99=%dµs\n", statsB.n, statsB.p50Us, statsB.p99Us)

	fmt.Println()
	fmt.Println("=== native transfer write-delay (send -> FirstShredReceived) ===")
	fmt.Printf("pipeline A (%s): n=%d p50=%dµs p99=%dµs\n", pipelineA, statsA.n, statsA.p50Us, statsA.p99Us)
	fmt.Printf("pipeline B (%s): n=%d p50=%dµs p99=%dµs\n", pipelineB, statsB.n, statsB.p50Us, statsB.p99Us)
	switch {
	case statsA.p50Us < statsB.p50Us:
		fmt.Printf("pipeline A has lower p50 latency, by %dµs\n", statsB.p50Us-statsA.p50Us)
	case statsB.p50Us < statsA.p50Us:
		fmt.Printf("pipeline B has lower p50 latency, by %dµs\n", statsA.p50Us-statsB.p50Us)
	default:
		fmt.Println("p50 latency is equal")
	}
	return nil
}

// giveBotUniqueWallet derives a child key unique to this one bot --
// via brain.ChildID(index) + common.DeriveChildKeyV2 -- and sends it as
// the bot's own trading wallet. index must be different for every bot
// uploaded from the same parentKey in this process (A gets 0, B gets 1
// below); reusing an index would hand two concurrently-running bots the
// same signing key, which is exactly what brain.ChildID exists to avoid.
func giveBotUniqueWallet(bot *brain.Bot, parentKey sgo.PrivateKey, index uint32) sgo.PublicKey {
	childKey := common.DeriveChildKeyV2(parentKey, brain.ChildID(index))
	bot.SendC <- brain.DoWallet(childKey)
	return childKey.PublicKey()
}

// waitForNativeStats polls path (the bot's own real stderr log file)
// until report_native_stats's write-delay line appears or deadline
// passes. Re-reads the whole file each poll rather than tailing
// incrementally -- simple and correct for a log file this small and
// short-lived; not meant to scale to a long-running bot's log.
func waitForNativeStats(ctx context.Context, path string, deadline time.Time) (*nativeStats, error) {
	for {
		if data, err := os.ReadFile(path); err == nil {
			if m := nativeWriteDelayRe.FindStringSubmatch(string(data)); m != nil {
				n, _ := strconv.ParseUint(m[1], 10, 64)
				p50, _ := strconv.ParseUint(m[2], 10, 64)
				p99, _ := strconv.ParseUint(m[3], 10, 64)
				return &nativeStats{n: n, p50Us: p50, p99Us: p99}, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for native-transfer-latency report in %s", path)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
