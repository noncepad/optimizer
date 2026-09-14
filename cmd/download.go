package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.noncepad.com/pkg/bot/solpipe/bidder/manager/bidder"
	"git.noncepad.com/pkg/bot/state"
	"git.noncepad.com/pkg/optimizer/prefetch"
	"git.noncepad.com/pkg/optimizer/prefetch/drift"
	"git.noncepad.com/pkg/optimizer/prefetch/jet"
	"git.noncepad.com/pkg/optimizer/prefetch/kamino"
	"git.noncepad.com/pkg/optimizer/prefetch/liquidity"
	"git.noncepad.com/pkg/optimizer/prefetch/marginfi"
	"git.noncepad.com/pkg/optimizer/prefetch/mintinfo"
	"git.noncepad.com/pkg/optimizer/prefetch/orca"
	"git.noncepad.com/pkg/optimizer/prefetch/phoenix"
	"git.noncepad.com/pkg/optimizer/prefetch/pumpfun"
	"git.noncepad.com/pkg/optimizer/prefetch/pumpswap"
	"git.noncepad.com/pkg/optimizer/prefetch/raydium"
	"git.noncepad.com/pkg/optimizer/prefetch/sanctum"
	"git.noncepad.com/pkg/optimizer/prefetch/solend"
	"git.noncepad.com/pkg/optimizer/store"
	sgo "github.com/gagliardetto/solana-go"
)

type DownloadArbCmd struct {
	ParentKey  string `arg:"fee-payer" help:"the file path to the fee payer (not bidder proxy fee payer)"`
	LogLatency string `option:"latency" help:"log latency to a file"`
	WorkingDir string `option:"work" help:"working directory"`
	Exact      string `option:"exact" help:"write comma delimited list of dex data to download"`
	Force      bool   `option:"force" help:"re-fetch all dex data even if prefetch.db is already populated"`
}

var defaultDownloadList = []string{
	"orca", "raydium_amm", "raydium_clmm", "raydium_cpmm", "sanctum", "kamino", "marginfi", "solend", "drift", "pumpfun", "pumpswap", "phoenix",
}

func (r *DownloadArbCmd) Run(rc *RunConfig) error {
	parentKey, err := sgo.PrivateKeyFromSolanaKeygenFile(r.ParentKey)
	if err != nil {
		return fmt.Errorf("failed to load authorizer: %s", err)
	}
	ctx := rc.Ctx
	cancel := rc.Cancel
	defer cancel(nil)
	dialer, err := bidder.CreateDialer(ctx, parentKey)
	if err != nil {
		return fmt.Errorf("failed to create dialer: %s", err)
	}
	if len(r.WorkingDir) == 0 {
		r.WorkingDir = filepath.Join(os.Getenv("HOME"), ".optimizer")
		_ = os.Mkdir(r.WorkingDir, 0o750)
	}
	var stateClient state.Client
	if 0 < len(rc.StateURL) {
		stateAddr, err := bidder.ParseAddress(rc.StateURL)
		if err != nil {
			return fmt.Errorf("failed to parse state url: %s: %s", rc.StateURL, err)
		}
		d := state.DefaultDialer(stateAddr)
		stateClient = state.New(ctx, d, 30*time.Second)
	} else {
		stateClient = dialer.State()
	}
	mDexPresent := make(map[string]struct{})
	if len(r.Exact) == 0 {
		for _, v := range defaultDownloadList {
			mDexPresent[v] = struct{}{}
		}
	} else {
		downloadList := strings.Split(r.Exact, ",")
		mOk := make(map[string]struct{}, len(defaultDownloadList))
		for _, v := range defaultDownloadList {
			mOk[v] = struct{}{}
		}
		var present bool
		for _, v := range downloadList {
			_, present = mOk[v]
			if !present {
				return fmt.Errorf("exact mispecification; %s not an option; options are %+v", v, defaultDownloadList)
			}
			mDexPresent[v] = struct{}{}
		}
	}

	// ArbCmd only ever reads the prefetch database that DownloadArbCmd
	// already populated at this fixed path — it does not fetch on-chain
	// dex state itself, regardless of r.WorkingDir.
	//
	// This must be the only store.Open call for this path: a second,
	// separately-pooled *sql.DB against the same file (as this used to do,
	// via a shadowed `prefetchDB :=` below) defeats SetMaxOpenConns(1)'s
	// single-connection serialization -- the two pools don't coordinate
	// with each other at all, so SQLite's own file locking is what's left
	// to arbitrate, which surfaces as SQLITE_BUSY ("database is locked").
	prefetchDB, err := store.Open(getDBFilePath())
	if err != nil {
		return fmt.Errorf("failed to open prefetch db: %s", err)
	}
	defer func() {
		_ = prefetchDB.Close()
	}()
	var botImage *prefetch.BotImage
	var present bool
	{

		pf, err := prefetch.Create(rc.Ctx, rc.Wait, stateClient, prefetchDB)
		if err != nil {
			return fmt.Errorf("prefetcher setup failed: %s", err)
		}
		mintTracker, err := mintinfo.New(prefetchDB.DB())
		if err != nil {
			return fmt.Errorf("failed to create mint tracker: %s", err)
		}
		doneC := ctx.Done()
		errorC := make(chan error, 100)
		wg := &sync.WaitGroup{}
		workCap := 1
		capC := make(chan struct{}, workCap)
		for range workCap {
			capC <- struct{}{}
		}
		n := 0
		_, present = mDexPresent["orca"]
		if present {
			wg.Go(func() {
				select {
				case <-ctx.Done():
					return
				case <-capC:
					_, err2 := orca.Create(ctx, stateClient, prefetchDB.DB(), 128, r.Force, mintTracker)
					if err2 != nil {
						err2 = fmt.Errorf("orca failed: %s", err2)
					}
					capC <- struct{}{}
					errorC <- err2
				}
			})
			n++
		}
		{
			wg.Go(func() {
				select {
				case <-ctx.Done():
				case <-capC:
					err2 := raydium.Create(ctx, stateClient, prefetchDB.DB(), 64, r.Force, mintTracker, mDexPresent)
					if err2 != nil {
						err2 = fmt.Errorf("raydium failed: %s", err2)
					}
					capC <- struct{}{}
					errorC <- err2
				}
			})
			n++
		}
		_, present = mDexPresent["sanctum"]
		if present {
			wg.Go(func() {
				select {
				case <-ctx.Done():
				case <-capC:
					_, err2 := sanctum.Create(ctx, stateClient, prefetchDB.DB(), r.Force)
					if err2 != nil {
						err2 = fmt.Errorf("failed to get sanctum: %s", err2)
					}
					capC <- struct{}{}
					errorC <- err2
				}
			})
			n++
		}
		_, present = mDexPresent["kamino"]
		if present {
			wg.Go(func() {
				select {
				case <-ctx.Done():
				case <-capC:
					_, err2 := kamino.Create(ctx, stateClient, prefetchDB.DB(), r.Force)
					if err2 != nil {
						err2 = fmt.Errorf("failed to get kamino: %s", err2)
					}
					capC <- struct{}{}
					errorC <- err2
				}
			})
			n++
		}
		_, present = mDexPresent["marginfi"]
		if present {
			wg.Go(func() {
				select {
				case <-ctx.Done():
				case <-capC:
					_, err2 := marginfi.Create(ctx, stateClient, prefetchDB.DB(), r.Force)
					if err2 != nil {
						err2 = fmt.Errorf("failed to get marginfi: %s", err2)
					}
					capC <- struct{}{}
					errorC <- err2
				}
			})
			n++
		}
		_, present = mDexPresent["solend"]
		if present {
			wg.Go(func() {
				select {
				case <-ctx.Done():
				case <-capC:
					_, err2 := solend.Create(ctx, stateClient, prefetchDB.DB(), r.Force)
					if err2 != nil {
						err2 = fmt.Errorf("failed to get solend: %s", err2)
					}
					capC <- struct{}{}
					errorC <- err2
				}
			})
			n++
		}
		_, present = mDexPresent["drift"]
		if present {
			wg.Go(func() {
				select {
				case <-ctx.Done():
				case <-capC:
					_, err2 := drift.Create(ctx, stateClient, prefetchDB.DB(), r.Force)
					if err2 != nil {
						err2 = fmt.Errorf("failed to get drift: %s", err2)
					}
					capC <- struct{}{}
					errorC <- err2
				}
			})
			n++
		}
		_, present = mDexPresent["jet"]
		if present {
			wg.Go(func() {
				select {
				case <-ctx.Done():
				case <-capC:
					_, err2 := jet.Create(ctx, stateClient, prefetchDB.DB(), r.Force)
					if err2 != nil {
						err2 = fmt.Errorf("failed to get jet: %s", err2)
					}
					capC <- struct{}{}
					errorC <- err2
				}
			})
			n++
		}
		_, present = mDexPresent["pumpfun"]
		if present {
			wg.Go(func() {
				select {
				case <-ctx.Done():
				case <-capC:
					_, err2 := pumpfun.Create(ctx, stateClient, prefetchDB.DB(), 128, r.Force)
					if err2 != nil {
						err2 = fmt.Errorf("failed to get pumpfun: %s", err2)
					}
					capC <- struct{}{}
					errorC <- err2
				}
			})
			n++
		}
		_, present = mDexPresent["pumpswap"]
		if present {
			wg.Go(func() {
				select {
				case <-ctx.Done():
				case <-capC:
					_, err2 := pumpswap.Create(ctx, stateClient, prefetchDB.DB(), 128, r.Force)
					if err2 != nil {
						err2 = fmt.Errorf("failed to get pumpswap: %s", err2)
					}
					capC <- struct{}{}
					errorC <- err2
				}
			})
			n++
		}
		_, present = mDexPresent["phoenix"]
		if present {
			wg.Go(func() {
				select {
				case <-ctx.Done():
				case <-capC:
					_, err2 := phoenix.Create(ctx, stateClient, prefetchDB.DB(), 128, r.Force)
					if err2 != nil {
						err2 = fmt.Errorf("failed to get phoenix: %s", err2)
					}
					capC <- struct{}{}
					errorC <- err2
				}
			})
			n++
		}
		for range n {
			select {
			case <-doneC:
				err = ctx.Err()
			case err = <-errorC:
			}
			if err != nil {
				return err
			}
		}
		wg.Wait()

		// make sure to put this last so we get all of the trading pair data
		staticLiquidity := liquidity.Create(liquidity.DefaultConfig())
		botImage, err = pf.Build(ctx, os.Getenv("REPO"), staticLiquidity)
		if err != nil {
			return fmt.Errorf("failed to get loader: %s", err)
		}
		if botImage == nil {
			return errors.New("missing bot image")
		}
	}
	return nil
}
