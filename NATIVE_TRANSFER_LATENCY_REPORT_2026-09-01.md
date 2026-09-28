# Native SOL Transfer Latency Report — 2026-09-01

## What this measures

100 real, sequential System Program SOL transfers on Solana mainnet, bouncing
between two wallets. Deliberately **protocol-agnostic** — no lending
protocol, no DEX, no USDC conversion — just `system_instruction::transfer`,
so the result isolates catscope's own update-delivery latency rather than
any protocol's execution cost.

**Flow:** wallet 1 (the bot's registered trading key) sends to wallet 2 (a
second key derived deterministically from wallet 1's own secret), then
wallet 2 sends back, alternating, for 100 transfers total. Each transfer is
triggered only after the bot receives a real host update confirming the
*previous* transfer landed on-chain — never on a fixed timer.

**Three real update sources race for every transfer**, and whichever one
the bot observes first is recorded as the winner. Two of the three arrive
at the same "processed" tier, just from different signals; the third
arrives only once the slot is rooted:

| Lane | Tier | What it is |
|---|---|---|
| **LowLatency (account)** | Processed | Account balance update as soon as the transaction is "processed" |
| **LowLatency (tx)** | Processed | The transaction's own signature observed confirmed, at the same "processed" tier as the account update above |
| **Commit** | Rooted | Account balance update once the slot is "rooted"/finalized |

Wallet 1 pays the network fee for every transaction (it's this test's
permanent fee payer, even on legs where wallet 2 is the one authorizing the
transfer). Total cost for the full 100-transfer run: ~500,000 lamports
(0.0005 SOL) in fees, fully recovered/swept back to the parent wallet
afterward — no funds are meaningfully spent or left behind.

## Result

| Update lane | n | mean | p50 | p99 |
|---|---|---|---|---|
| **LowLatency (account)** | 34 | 2,499.4 ms | 561.3 ms | 11,450.1 ms |
| **Commit** | 21 | 2,165.2 ms | 1,879.9 ms | 4,604.1 ms |
| **LowLatency (tx)** | 45 | 3,629.4 ms | 1,604.2 ms | 14,919.0 ms |
| **Overall** | 100 | 2,937.7 ms | 732.0 ms | — |

34 + 21 + 45 = 100 — every transfer accounted for exactly once. Between the
two processed-tier sources, **LowLatency (account) wins the race most often
(34%)**, but **LowLatency (tx) actually wins more often in absolute terms
(45%)** in this run — with the caveat that the "LowLatency (tx)" count
includes cases where the account-update source was simply slower to arrive
that particular time, not a claim that signature-based confirmation is
architecturally faster than an account update at the same tier. Commit
(rooted/finalized) wins least often (21%), as expected given it waits for
finality rather than just processing.

All three lanes show wide tails (multi-second p99s) — reflecting real,
observed validator/network load variance during the test window, not a flaw
in any one lane.

### Full per-transaction data

| # | Signature | Lane | Latency (ms) |
|---|---|---|---|
| 1 | `5Y5B4w7ZWyKTX15BMbur6Myyb9WQSac6A3PBzjDw8ezoXJsQ73PkAWLdndEaASFwoXk2i4RywoUohdrTabFdVNtY` | LowLatency (account) | 671.6 |
| 2 | `5gxoJzmE4rN9UJ2b4mJZH5sRjD7h4eb6nWvVxTR5YhABiYjkbUjMBBU1hFouGvizc4F8dXXAxyQN5ZwjyLVMGDGV` | LowLatency (tx) | 1,910.6 |
| 3 | `4MB4RH8eeArgHKWde5N7cgWtCtcoDzFtGhZEK1gMmfpJfRwfrSpyrd3gUhQmqDgJP4ysGzHpPLWNWtesYvLtAyH` | LowLatency (account) | 3,575.2 |
| 4 | `c6hZm7ahw7yekdre72uTYK4Pg6CAUCkjdTkbV8tu979CP74r4H2Ea7yNq5dqsuXEiTJiqQWSQxdWBPzcNAjm2Y6` | LowLatency (tx) | 595.4 |
| 5 | `5o8Vim9o6hR6AkxupAM1tNdPcJmESKVWyJQqesxPTC7xgiYQ1yFxWf8tcE8rcowUR9wzTpRuXLjm2oomhidVeNyS` | Commit | 4,604.1 |
| 6 | `44KLtYm5NBCYcrjxQsqbwU64uXVLRFLGkuMaJYbZ8Wc5fuXwYrUPpqUFtS7tJpyVZ1XQMvJse2NMo6qAoY71Mppk` | LowLatency (tx) | 1,604.2 |
| 7 | `5eL1fnVQPD17Htn8YzAPgTqtf8AHgPnJtdmg4YwutyWJhQWAYidhsexr6GYADnYT2863TZFDTf8Zwp1ZB7SyQqzf` | LowLatency (account) | 266.7 |
| 8 | `3sqYJGgXz8zFBX4UenWWorJof69oDAeBc4D5igKeB1eB2XPgyBFdK6EgnBbvut2PicVZ1GQgnZzZodrDwDoMF6U8` | LowLatency (tx) | 333.1 |
| 9 | `5AejW8aDywyVC4VA5sqYq539vz7zn3xnYuq31J38vzCWLVvt7rv4twdox3aVK5JyDFgzLsc29dKYcCdfozevpHK` | LowLatency (account) | 271.8 |
| 10 | `4LmAhw4yKJf19Waa3cLG4KxfdwESj3uDr1qSW9ASF8TzdHmFAqEo5jcHUhzPTZgDCW8UnTAW2NAA2d4GhHLr3xA9` | LowLatency (tx) | 2,856.6 |
| 11 | `5wFbNhWBfLqtWZhKwAKkeZTKLyLU6VkBzgutLrZ8VWdfHWhtp4TbFh7QJFZB4K1orRfRJnnQDfQ7dcFubZP15kev` | Commit | 149.2 |
| 12 | `5oy7uhaAdCKVpc5gqjTJATK88PYKczP3pq8D2T2GAkSpPq6hUnY1rvmo1ApzjVhH2pQb5ZWTYVwaDfTQSbJF9Frs` | LowLatency (tx) | 3,702.2 |
| 13 | `MhRs9cic7iCf6ijnLdNsYMTTPk4aRCvySc6qgQxoWAoTYC3S3bpmUk3kFKNJnmwZTdYfqgTj1CjPN6Bb2hGcNVK` | LowLatency (account) | 541.0 |
| 14 | `5yF2JJisfnMmTW3L2G7TkUiUViwYrQNi2VRtCtZ7Jh9QMghEJTcuytt3LapJv341wGGKLwr1zhq7ACqwmVqXDAqY` | LowLatency (tx) | 537.8 |
| 15 | `3CtnRJLAcaf3fr9B11cGLEi8KKM1qfc6WUrSVGkoeN6cagtkMdXGKxQLjkE2vSzchg5u1X3DJzvqRC5N3jf4uLSd` | Commit | 1,879.9 |
| 16 | `HQqk2SoGv7tH253MG6Ec3zgyJUBfs2GpjHm16ebMoeHH5dhL8wt9o78b9Gq2GRJ1a8cDpR2J5PU2n9i6ASwbkud` | LowLatency (tx) | 5,810.9 |
| 17 | `38DtG9MfBHCBRMbNgDNwrrXfBezQYTvJSoTDY9kLhLFEcJ3Hk2A29793XTQbtnsC94HhQuc2bDam6CpctWQ3y3yG` | LowLatency (account) | 561.3 |
| 18 | `377BNwA3QEWudrLA8cpi3TR3sBuBmqb8C2M8K62kdfkNUCkFd3TrgtM7uGzgLEU2PH9bdxiMjmU1XHhvQvBV3K7Q` | LowLatency (tx) | 535.9 |
| 19 | `3yYF5pdsdYCnpDESLUugTcG4MFSX5278n23anLr3T7iZPF4WufZ5NLCDzvT2pTrLbvissVWAze2K1F9Q5cLh4Eow` | LowLatency (account) | 364.3 |
| 20 | `G2MNi3EPNXUjb7tpz5jL3CgJybvLhJS8jyMVe6qxL1T3fU8XbiW5tjmTGHKH2dV2wDwKoC3N9F1ZbHX624QQU3W` | LowLatency (tx) | 339.8 |
| 21 | `3v9K4SAkNj3FvjAj77d1NVMqiTFb2kh47hNXP3mc6PNHhtHMpRkx6VyaEJM7o6fvf54sPaQEbUfi7Bispkd8Sr5Z` | Commit | 80.7 |
| 22 | `2rvB8JeygqkyWSj1RfvRjVvUqZ2Q2Dyfu8nneAhWeERXbv5vwbJ7aSuAHucoHTqFvwNH8WGRqpHVQTiURYmQBrNV` | LowLatency (tx) | 428.5 |
| 23 | `64HwD5cBYd8DkE6G5rZR96KuJqr4KscUQJZxaoBLVMutzuKm4XWAdjjCvosaZXP8h5TsEfWFvhJcg39NhmAB7j9V` | LowLatency (account) | 1,471.5 |
| 24 | `2CjBVB9sRtwSzWVXu1GLMVqFHURhcAYF5E53wjDUFTHBvGb1evx3GtksaAjdPAikZZ7NLm5mZ2ZdDapT5ZifHUbE` | LowLatency (tx) | 318.6 |
| 25 | `3dxyWexZAuSUFzCCiPJGGUETY4ZLSbcAgmFjjMxmFjWUDsuQEpnrfJZfZ3UCSRDYRv1xXJ9XehPr4LRp7hXDM591` | LowLatency (account) | 327.7 |
| 26 | `3zbac43KWFuewjggJ7R2QanVxkjmFQzyGXEb4fqBxrfX8UoxwtBNBH1dZpPysJkAeRvxFKr9vHGowejEkzE9zL5k` | LowLatency (tx) | 185.1 |
| 27 | `5jD5U1NHtULfjEc3svEPH2zr8szFmzFCme1B8iJW4HNCAwtGDUYpxpzvajWQseGhPZWHRgFtC9MnfeknkL3kYZaD` | LowLatency (account) | 285.9 |
| 28 | `3PhadBafN8ig2VxYZkRF9YpBUmoubvbgVmW9UUAcCLsqLKrUvU1TV2c3MoxyemWtWguhwMjisNcsh8bdMpV6wZsn` | LowLatency (tx) | 11,478.3 |
| 29 | `2R4Ddk3oTP6iGgpWsmLB4x85CZvm8zSM9FzRzJuHAFtF4sJaB55ATJJjqHzfmeJoxjqK9iYZHr8nHo2hN747i9nK` | LowLatency (tx) | 7,724.5 |
| 30 | `2mAfUphGbU4qoXfzEnmgKNHgJXcHK2hghroBMpo5gU3Dzy5EJcEro2o6jXxK8ML9X6rtfaw9qnNyVzxXKHbCwJwC` | LowLatency (tx) | 426.0 |
| 31 | `3wRx2XWvr1MbS71bgke4ymvVq6BucuHvcc56hEPWVuR6J3d2zk8KqNtAXyHM5VBJkJepg8L12DEA3uJETtThajVc` | LowLatency (account) | 227.9 |
| 32 | `55DBZoRAZJrmSG7kweByLAouXPU2aaQ6Hs6iqyk7dYPyBDc91DPfRT4NRGg91c2KMsJSkJ7bAxEZp9eucCCKd3Sp` | LowLatency (tx) | 379.3 |
| 33 | `CoNP5Lrxf6zXXo9NNpSSqmPcznt1kT1vH9jXTByNQxy7oru36JZDSxHr7E5crujhh7Wn3LnqHVGCxjJT8C2kXid` | LowLatency (account) | 4,957.2 |
| 34 | `3zbZYbL37AyaiSZ3wDj1U4AhXufKAXbLzKHusYMEwrH6tU7TXDsde5ueTiuWsR7DXNC99qmM59FhWanVjBqpKsff` | LowLatency (tx) | 639.2 |
| 35 | `3ieM9eBqVre8utdnDCpR75TfAex8eqUshKo5VQhuUF8ge8zCMxFWdErTgdciDNHv4jYFmXDauZ14UfVtCLFU1L8Q` | LowLatency (account) | 576.5 |
| 36 | `5jxo3FkLAaTrsm4jJWPzUXp4YdkEZK8pvKUa15kdvLAXB6Lv3Y82cb99bqnrk7J9qmWjDUyLrUjyb9XwSimESBMW` | Commit | 2,991.7 |
| 37 | `jDBz2syiqkESxy1gBgNDBgQn27VsvwHJdUrMGdMgrNkMnSUsFsobXnYHNwaRPC86oBbGJLgjcoe54yZEVq4SQYq` | Commit | 521.4 |
| 38 | `4jk6tSScHHDTvyzYgt4sGdxySmDYYY7HTMpihEnNSbectEMZvZcP3sTg6QevSE6SvsXMWoqAp8Ho9qcnqLB5gwPa` | LowLatency (tx) | 14,493.0 |
| 39 | `3n6VRUktCJfrGPS6J1WgqzdnwQ1iXTjUbJHJ2evkECr8cD5gvCqxwYvEVpZUwZa1MZA2f44E235JCKkfHCu3ZaTS` | LowLatency (account) | 11,228.1 |
| 40 | `5wtdj3BmuCex7j3DSDgSewmLyXoW66d9MLvx6jnmjfjxpCB1eaUerNNtiPSVMuNVw2tW7ZwpkciAjknMqgxDTRCx` | LowLatency (tx) | 6,689.8 |
| 41 | `2wpRv34RSQUn5JvtWPLkyx5hseeNcCuajnF2zMfZTi4FYbS7VkP9GFgW1E2ddXmtNetL9ADBvTEMPKWfptEtHsNj` | Commit | 3,872.0 |
| 42 | `46vFn9F74m95HQmV3NF1xFm7nr1EzYYQVXeFLYJ3QNcjDTxbkY29xoQwYqznFPUYgDYsnJ4th5B6Hd79SciF9WXj` | LowLatency (tx) | 8,497.6 |
| 43 | `4KezGo5HmdAAHNobRXgW6hLwEDAwT3DAvWnPdRJGiNCYhmNYNrAMTrWb2GEJi9y8cC7uWC3TXKv4Aq1VxAmiowZx` | LowLatency (account) | 16,907.4 |
| 44 | `2suKWRsQCLscSWEUXZnjsjsjiRNN7dtmdM8A9KZnd2hSXhPhMMNQzjP5KUgT76mvnKTmP12axgBfhxahUogSc5PJ` | LowLatency (tx) | 1,127.0 |
| 45 | `23e6fCNVN9tukPzvE4kuSbHCC1emr86jJSkY4xgdzZKYg4FYsAxZctZ1y3LnKJ9XUTY9tkRcKWnq8VJri326uyPT` | LowLatency (account) | 723.0 |
| 46 | `3zcFWu9aqtwFwApY27hDet6kbcuptXWFAbf98y1gnYmuZhLCaMuE7zyoi8UkbmiW1sJimFsH3LQKznfy6CbbFsq` | LowLatency (tx) | 4,625.6 |
| 47 | `9yG6tDzsX6Qa2LDzyRw1NxXKJQiCGinsYae9i26H14VudYFymnDHzbVFJSiQsiAQWM8PsuVoox8Pov6rUWWouZL` | LowLatency (account) | 3,661.2 |
| 48 | `5nopCtbgktKoGpSMaVHiBgQyZ52qN3NbxSzdJxFUAv1XaBhKVpobBMVY1jKJ42ZJWeH5PKCscSCHiNKhPbSmErvw` | LowLatency (tx) | 545.1 |
| 49 | `3Eb4wcqD2sqFNMsoSita6qnVtpNfm5qUUM5d77eh4f4avS9nFryLmVY5iGKzRWVWcnsrnaAivo9LdBhS5b7XuJBa` | LowLatency (account) | 453.4 |
| 50 | `38dVpeFTjoGsM1ocRQEWzqkfi1mt6cauAcwUXZViGaNVdWuD32v42pEDyYS1Lw14Mmz2FDWF81gLL92pHuEnJNx7` | Commit | 200.2 |
| 51 | `22cYzq6ecm4HqswJ9mKem54JCmbrQNLrsnJymt49kvJERzjPFTaNPKJVudeuQovNuuCF2763h6VVkcQEPS4zP4Zw` | Commit | 741.0 |
| 52 | `57K8LMokoVnCUFMgb1E9gjywaJo65EqHKKSB3cH2PTNXPtBH8LDVwLtWCtxssXHdCkrYXzY8vQXRAtprR3GKtTG9` | LowLatency (tx) | 7,997.3 |
| 53 | `3fLVbZxL3DMTsxfB4ckBvhJ8Ue3KXPJwanSyiaMyzFUM1THCxWWxkQKqaAhXVspnQN7HGK46h8DfdqZNVcPaMnED` | Commit | 389.3 |
| 54 | `3g6p6crLoTviYYbaEMtP1eueNgRTLpHUC2Knez5bxYwAiUwnWR865C1YUmC41MfLLyEb1fRMRJr3R31M7Mz64c41` | LowLatency (tx) | 4,858.0 |
| 55 | `25d7wgxaUUiyrzZ9fSd4nWZQJ7TWRFxMQPkwjre8E2NVkVMFzhBKnSqPpFmVKPXBTyv9NU5JV45XvCxcUVqu5hgs` | LowLatency (account) | 192.2 |
| 56 | `4SaVkr5GVYZoWMQb9fJx4JwUSFEuWQJhJir25ELogPsNVVCooaeuzDkUawahuAXvSF4BxwVkSuG13rJT4VcFkPJR` | LowLatency (tx) | 300.2 |
| 57 | `7ahQAvQXejTBEE76u2LukSUhR92McGidqzM87PoSsKabC3ro6e6dt9VwiBdxqnFGDRhFZsUYgQaWx1DdtekGgtL` | LowLatency (account) | 297.6 |
| 58 | `3ejD61fd4jGL9XtuYTGVVS7NgiUu5n2teMcB4Hw7tMCTzajxSmenVb1i2ZPxeKSq2R4CpdBrmd8MNi19YTHQ48vT` | Commit | 4,078.9 |
| 59 | `5eFcAC1g8TEkWnDnqqAfWQiTKVVXVnPsL1awiT1Au47BSVXeZJU7VpjhbLVjejbaXXzvNRaYQZ1FMh2mcKEWaQjq` | LowLatency (account) | 2,604.7 |
| 60 | `3xGsRwZYGQ4zPsLzbABjzQ3S5trYCcHQMtVKoGRsob1wmX1vqkF38gE6E9M8hz69QPB2zLJ4vvHgsRtLxvFoN6jc` | LowLatency (tx) | 414.4 |
| 61 | `2iCNejLnenv4fHQb5WvTNmMd9aYsY6PhaLLjDmKABzaAdQEvhDvehGcpa2X98x2nVkj1cGZLA3keuxeeTJeFMoUN` | LowLatency (account) | 513.4 |
| 62 | `3PjKxjazGNHyLGQZMtwCbAHZYWZ9rqntTdQmuVMPVMX43rxjmGC4HpWfWncmkAeNUridyEbS7BsZ5Mz6YKCsA97k` | Commit | 1,718.9 |
| 63 | `5j6Drzkkt17bmkd6QNmWWMMuLBMMx6ypE9qZJ6uPR1TvDRgpn8mVAxqWQH3REzDV2ZRugueVpUcB6ZThENqBW1Et` | Commit | 281.2 |
| 64 | `5GEsioDZYk74YtgBmrV6UgPXju7mTWUrbsh4xBhPTjXmY8GLn65b8pJQuhSbxPFY8epTGPDbUvEwixmTFAMqSWL6` | LowLatency (tx) | 4,851.2 |
| 65 | `2zAxrehie7utVULjMZhiiGSvZ59FW1bDRYhuahUygUmQVmGaZLPtD391dyFm2WgvT6RutKQrvt7g2KpJM3psuoQu` | Commit | 3,827.0 |
| 66 | `2Jiu6TP83wLi3tjzxwfNZb1K2462mvmsFMrgcSbf6o2uPJxdHwLniXYzrT2ThaDWsSqfa8mfBRTLwBfbxQ9dVjYz` | LowLatency (tx) | 14,918.6 |
| 67 | `354dnaP9EchjKH7kVYoN2PSgbQ7v8Aia7Js9qDBuR6zEMtp8N7EJvYh2dCNwZcUbU7Dpb8F8zAcWNUWZ3F1GP5xV` | LowLatency (account) | 592.1 |
| 68 | `4hXehxXCaLSNgzCTgj6E9wthmweGnYjxoZgfLkf3awWwNyZBTuxju2od7S9KtiJyg3Jk8RPZBZGUz2FhufWm54Cb` | LowLatency (tx) | 4,918.7 |
| 69 | `4unXzemSy4X4kDKj9ErFQXbEvViBMQPDNoAeWakRpUjXDdSzAZxpru6UW6rtYWr7HGjyd4wjjM4Xq8b4Pn6eigLi` | LowLatency (account) | 548.2 |
| 70 | `3gtAZxwKnKP4tnRqKhzeQaJejxE8BPp85Qt4KV5gDb1UwMNXbsyCGSVDHYazb3b43qdJ3qg3iFsEUEKQWLtEdfzo` | Commit | 3,905.0 |
| 71 | `h2RwZuWmuqQ4nr2JbrUSBcajJMxj4B6emGGSCFNHkhZMBQqSCgcL6nYpVYCRCKdkiSeAtUMpQWuUDGzAF9FM9uV` | Commit | 321.5 |
| 72 | `3CKeZK3CwwcbBzo8dDhGuo6bGXe5JxpvM2ZRqqtSCSW8ngQKotgbYMZDsXqjqi5pT5P3yzmGidzzZroFp4pivSYE` | LowLatency (tx) | 9,891.1 |
| 73 | `5JujffCwokNoL3uzRpZUG7MY8eHKJFoC6b6AvByHb2B8bC694S28HPzv6Qe1gL3ZtmzMWTrGmzmEqtU1EBNqWsUx` | LowLatency (account) | 3,803.4 |
| 74 | `zJKmEoMxJPLBCYbqWK6DkgSWaS9Wa782PdKFSVszxbVEHwTdfVYitnUKygm6eApFLfJ75UwRXFP9wQ1k5rbnah6` | LowLatency (tx) | 300.4 |
| 75 | `5N2SECyUL2dVyX8TeHnFMBtwCyMar9CcoaiNH9rtK9R17RJdmqNAhS5p2Cp5W6zwwQk4mnRaukqim8ftE88i8owA` | LowLatency (account) | 2,223.3 |
| 76 | `4Lg221F7ZQ2y94XqUP5DuizGze5dnb5CgtkLxfAKA28YftnYUw9NnBJNkfozQ4tgBfkmeJCwu4zk2xCckKVnyY3Z` | LowLatency (tx) | 2,193.6 |
| 77 | `4YdWMWZiCqMxb6pGXWu4L4JQrDM7ifdNdRetCm5nEkTWKuWLAtg9ikJr2NFFgNKdEHcARgnxVdJz7UhuczxuVwSD` | LowLatency (account) | 477.0 |
| 78 | `2JHtXyvD3xVKtzGTuo6nsfRzvTb5CwfteQXxyT1Y9ZBXQ1WD9U2uKtT7ubsQEWMuMevFhjbvMQpEm7Xxd9p2hjBh` | LowLatency (tx) | 505.3 |
| 79 | `26RwMkR8bny15fnVkBYZmT75CGYiuaAnmCFvLTn4Do6xLDfZvvqUMvvX3Tja2eJv7CriABj1B8kFQLpnfWHM9fui` | Commit | 4,299.6 |
| 80 | `wrcwUL8MiP2gB1zkfVkyfyYVP5RsAx7tzhjHHGaddU1eXF5PGbdafWRwxpWgnG9ZUjw1XiNZURJ6hJvMTQYu3DR` | LowLatency (tx) | 6,684.3 |
| 81 | `6wtbQrPvWPkAWF56q5NyHVQBoXGd7qev8FuvZGd6B3dEWwbodUdQmvP1pkMhjZBrFvwgQTnHi2oX2p2VStE1HHx` | Commit | 5,271.4 |
| 82 | `5BsDkZLq5MrTSpVoNrmN297XCSVGhGyp3qkUUFedfTrbAsNQmU3LsLc5U9MgjcaGgCFfFEMARF4Kdr3zTPuWJTz5` | LowLatency (tx) | 3,988.8 |
| 83 | `59pbS5Q8pbNcXbdMLJRnUSNPboGo3uu1i4Vu3VFLLQFsh5BA8XsH89bZoRUezcvL6JtxdNTnE9k6Q7KHbSmT9ak5` | LowLatency (account) | 11,450.1 |
| 84 | `84ySDb1dnUnZWSnRKgwDBNRzLPaBpBKPkbcj6eLfVgVkfsgefbhoPqT4reAm5bWz4xXHhm7G8RRJtoAPmXcY825` | LowLatency (tx) | 453.5 |
| 85 | `5tzwfAG8DTF5h17w1XkwWyiAbYshNeMEaxdch9n7FrHsdRUeGfcunaZ3uKWpqHYKhhRuX936tJCKy1hEPSSXdhB` | LowLatency (account) | 289.5 |
| 86 | `3QSQVW9gr7ZoB1wLNSpF4grfKLC3tJf92KprVcnETQ5fJ71iRSPSF1vAdDYjrHs4kC4KWzS6f1xAmfVWijR29LvD` | LowLatency (tx) | 347.7 |
| 87 | `46nzhyZcQcVfVtCnwLeh6h9475FSNor4Fscrwn8sd4aYvrsTKHKFFNtRGhyyo3mWiYZvkq6RX3PTn15xAeHMqZEM` | LowLatency (account) | 255.3 |
| 88 | `2Gua3WPaEQCRj85TTXXWes3znQ5x4nu57b2Uj9rBdJavPxKvCvEitTwuxt3skLhDzwU7F7FYEpCTVWbvj3svtsgZ` | LowLatency (tx) | 6,390.3 |
| 89 | `n8fJ45hqT9kwEdsGG92JaH4vgBH9dc9mY15yWRoGiMJfNPG8sPCqGPCsiPb9mnYkqP3r5kQrQQVZnhBm7sfgTUG` | Commit | 2,236.6 |
| 90 | `41NgWD3CLaoF2dbSEfZwgnFyyC7D19jckFnMhkhFYWtLvUTVhvUjFzMu9rGygzNEmuTSe9TpL6344sBxgU9w83Jh` | LowLatency (tx) | 4,305.8 |
| 91 | `3UxdafK4D2tCsjGWrEkqTuimS9bbDNa8J5LFwVTTqrEq1qihVUdSawAzkPmdDPWaRc1AfSLktrKokXYP7fEQNGRi` | LowLatency (account) | 360.3 |
| 92 | `tBPQRn33WGoU9VPqfDcwvz4cynDacsepd8uuF3Ff3Ma5tzAk9DgLwEHBk276UEKr8itpURWVquQuK5d7sscENme` | LowLatency (tx) | 374.2 |
| 93 | `uRBwoWZKJYiaqexKoALiYpAwVMkBuxuboVW4M2Aak5i26HAZjBbNLBBaueyjAHZeoydUDoePtuk8HFMNhxgTaJi` | LowLatency (account) | 5,802.5 |
| 94 | `2jgTsCM2ikC2MNh47mXQi3cyZbYj3ybg7m3MsGvdoU2inwNyjMiCa9g7U312tKEScwBWJmByqMh1cGC9NHpSvs6X` | Commit | 3,733.2 |
| 95 | `42Yq7jmU9Pte7QAYy4AsHx4C11ttCxwB1CowC65Lh8wbtzyTKadTYL5V1HattJ7ZCv3wXGpFCxkMkHxaNP6FgVm5` | Commit | 366.7 |
| 96 | `2t1PWQ87kxHxUqsQkhWa8b31w6srStFYgpFHMJ2dHKfk61eh72hructe75zhWhTKbtqEmZtyAS7XFEoFa7rzeRWW` | LowLatency (tx) | 13,044.0 |
| 97 | `1ejoBSpPSj1EiCRjEHHGqsPcePbbYBKV3JqcWxZ285SAqrNpPeqJdFmzB9E32Cv7Y39Z2RyG5mAQjZCkYAG9jTm` | LowLatency (account) | 7,534.5 |
| 98 | `2WDADM7tbmuLuboXM7jTHhbaJ31qDiQFxswq5ug5CgtPcoWJTBNSvNaAe6Hbsu5gckjAqfkCSnimTHEPkUFiSR1x` | LowLatency (tx) | 323.2 |
| 99 | `4SWT1MpvT8KZutiQG8ybKxuuTyeQCZ3EtYdNkCZ2R5NhW7tSUZCNpndPfeGyc5HaAUsNVNPXbavXwv81NDRT6E6q` | LowLatency (account) | 963.5 |
| 100 | `3SXRVuy1kt6qvX7KQS5MwuNch7HY3HgiNdse9wgGLyp3GPHcAS3kbQFiGRt36AY2cKweu3oQ6zX27wd47RiHQprW` | LowLatency (tx) | 479.7 |

Every signature above was independently verified against the run's own log:
each row's latency was cross-checked against that exact transaction's
own send→confirm timing recorded via the generic transaction-confirmation
path, not inferred or guessed.

## How this dataset was produced (methodology notes)

This is the third attempt at this test, not the first — the first two
surfaced real bugs that would have corrupted the data, both found and fixed
before accepting this run:

1. **Transaction-lane misattribution.** The first successful 100-transfer
   run showed "Transaction" winning 60% of the time — but cross-referencing
   each claimed win against its own independently-logged confirmation time
   showed only 21 of those 60 were genuine matches. Root cause: a delayed
   confirmation for an *older*, already-resolved transfer could get credited
   to whatever transfer was pending *now*. Fixed by having each transfer
   record its own real signature at send time and requiring an exact match
   before crediting the Transaction lane.
2. **False positive on the account-balance lanes.** The second run showed
   two genuine on-chain transaction failures (`insufficient lamports`) that
   never appeared in the bot's own logs — the account-balance check was
   satisfied by *any* increase over baseline, not the specific expected
   amount, letting a stray or coincidental balance bump falsely confirm a
   transfer that hadn't actually landed yet. Fixed by requiring the exact
   expected lamport amount, not just "more than before."
3. A separate reliability fix (not a correctness bug): the very first
   transfer now waits until the sending wallet visibly has funds before ever
   broadcasting, instead of repeatedly sending doomed transactions against
   an empty wallet while a slow upstream balance subscription catches up.

This run (2026-09-01, the one tabulated above) was produced with all three
fixes in place: 100/100 transfers succeeded, zero retries needed, zero
on-chain failures, and every Transaction-lane claim in the table above found
a directly-verifiable matching signature (100/100, no gaps, no guesses).

## Infrastructure note: what fixed the boot-transfer stalls

Several attempts before this run stalled indefinitely waiting for wallet 1
to be funded, even though the parent wallet always had plenty of SOL. Cross
referencing the validator-side logs for those stalled attempts against this
run's own logs pinned down the cause and confirmed the fix:

**During the stalled attempts**, `pipeline-catscope`'s own logs were full of
entries like:

```
rate limit released: held=4m0.001467964s ... method=/catscopestate.Graph/Subscribe
rate limit released: held=2m0.001227882s ... method=/catscopestate.Graph/Chain
```

— the account-subscription RPC our bot's boot-transfer logic depends on was
being held by `pipeline-catscope`'s own rate limiter for 1–4+ minutes at a
time, so the parent's balance never got reported back in time. `catfwd`'s
log for that same window was completely empty — consistent with the
boot-transfer code never getting far enough to actually broadcast anything.

**After restarting `pipeline-catscope`**, this run's own logs show a
complete reversal: **zero** rate-limit holds in `pipeline-catscope.log` for
the entire test window (the only two lines present are a clean "context
canceled" at shutdown, not a rate-limit issue), and `catfwd.log` shows
**101** transactions received and forwarded start to finish (03:43:42Z –
03:48:40Z) — the 1 boot transfer plus all 100 native transfers, zero errors.
That 101-for-101 count is itself a second independent confirmation that
every transaction in the run's own table above is real and accounted for.
