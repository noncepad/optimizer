package phoenix

import (
	sgo "github.com/gagliardetto/solana-go"
)

// Market is one Phoenix perp market's identity -- just the fields
// catscope-rust-bot's PhoenixMarketRaw needs (live pricing/risk fields are
// fetched by the bot directly from PerpAssetMap at runtime, not cached
// here).
type Market struct {
	MarketAccount sgo.PublicKey `json:"market_account"`
	Symbol        string        `json:"symbol"`
	AssetID       uint32        `json:"asset_id"`
}

// fixedMarkets is a small, hand-curated, fixed list of Phoenix perp
// markets -- mirrors the earlier phoenix.json's exact values. Every
// market_account pubkey was decoded directly off live mainnet
// (PerpAssetMap at 2nHGAaEw3D5dd4hVueaUNoygkQFmoeKqRQWnSPqSMFUC), not
// guessed. Phoenix has ~65 live markets in total; this is a curated
// subset, not full on-chain discovery -- see Create's doc comment for why
// that tradeoff was made here.
var fixedMarkets = []*Market{
	{Symbol: "SOL", AssetID: 0, MarketAccount: sgo.MustPublicKeyFromBase58("71Si24E4uc3oCaPbPZTozC1ptSNNqygjjebxSmErSsC2")},
	{Symbol: "BTC", AssetID: 1, MarketAccount: sgo.MustPublicKeyFromBase58("AXFz1MuzMUBHi5UKJuK3FDCQ73o3rSzubGU2mPr4LLU7")},
	{Symbol: "ETH", AssetID: 2, MarketAccount: sgo.MustPublicKeyFromBase58("9u7aqptdRFbsnnoHtjK13E5JkeM14EW5fAKTRPidVF88")},
	{Symbol: "XRP", AssetID: 3, MarketAccount: sgo.MustPublicKeyFromBase58("CwaHtR69D287PLnoX8zzzk1zqwNQKVJiJQTNs6JrVyna")},
	{Symbol: "HYPE", AssetID: 4, MarketAccount: sgo.MustPublicKeyFromBase58("CEx9Mgz5cAmWwy6D5H2xGrhmwKeC5L7KtYhFhVGtiBaZ")},
	{Symbol: "SKR", AssetID: 5, MarketAccount: sgo.MustPublicKeyFromBase58("HnKUNWrcrpYqK5Uworv2wH6Ki3ciDVDDYqicE5SRAAWs")},
	{Symbol: "BNB", AssetID: 6, MarketAccount: sgo.MustPublicKeyFromBase58("4Xd7E3eBXazSoudAMACsNdezA4UuJETURL1S6fqpjcTR")},
	{Symbol: "DOGE", AssetID: 7, MarketAccount: sgo.MustPublicKeyFromBase58("Hqt5Vom5L3X3RcKysKnBmLAvjYgo6TGAwoheW658h5cz")},
	{Symbol: "AAVE", AssetID: 8, MarketAccount: sgo.MustPublicKeyFromBase58("A6CdPuNY3mp4tsY1ifQoesHKfwYdVv3wqcaKirEGtaMS")},
	{Symbol: "SUI", AssetID: 9, MarketAccount: sgo.MustPublicKeyFromBase58("GQS3qn9EjKYpqoLwpvPVbeW9o1bjFYhc1SRMZYw4iNru")},
}
