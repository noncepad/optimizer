# Native SOL Transfer: Write vs. Read Latency Breakdown — 2026-09-01

## The question

An earlier write→read latency test (see `NATIVE_TRANSFER_LATENCY_REPORT_2026-09-01.md`)
showed the full send-to-observed loop was much slower than expected. This
report isolates *why*: is a slow loop caused by the transaction being slow
to **land** on-chain (write delay — it misses its first leader opportunity
and only gets included one or more slots later), or by the update
**propagating back to the bot** once it has already landed (read delay)?

## Method

Same 100-transfer native SOL round-trip test as before (two wallets,
alternating `system_instruction::transfer`, no protocol-specific logic),
extended to capture, per transfer:

1. **Send slot** — the freshest slot number the bot had observed (via
   `Event::SlotStatus`) at the moment of sending. Approximates the first
   slot this transaction could possibly have landed in.
2. **Inclusion slot** — the real on-chain slot the transaction actually
   landed in, read directly off whichever source resolved it: an account
   update's own slot (LowLatency (account) / Commit) or the confirmed
   transaction's own result slot (LowLatency (tx)).
3. **Write delay** — wall-clock time from send to inclusion, looked up
   against the bot's own local slot clock (a record of the real `Instant`
   it first observed each slot number) — a measured duration, not slot
   count times an assumed ~400ms.
4. **Read delay** — wall-clock time from inclusion to the bot observing it
   (total latency minus write delay).

## Result: write delay dominates the tail

**Write delay and slots-until-inclusion are properties of the transaction
itself** — how long it took to land on-chain, independent of which read
channel later happens to notice that it did. Both are reported once,
globally, over the 80 samples with a trustworthy inclusion slot (see
caveat below) — not broken down by lane, since the write path doesn't
have a "lane." Read delay, in contrast, genuinely is per-channel (that's
the whole point of racing three of them), so it's broken down by lane in
its own table further down.

| Metric | n | mean | p50 | p99 |
|---|---|---|---|---|
| **Write delay** (send → inclusion) | 80 | 2,190.3 ms | **120.5 ms** | **19,169.8 ms** |
| **Slots until inclusion** | 80 | 8.4 | **1** | **66** (max 73) |
| **Read delay, all lanes pooled** (inclusion → observed) | 80 | 406.4 ms | 293.7 ms | 2,426.8 ms |
| **Total** (send → observed) | 80 | 2,596.7 ms | 428.4 ms | 19,439.6 ms |

**Write delay accounts for 84.3% of total send→observed time on this
subset; read delay accounts for the remaining 15.7%.**

The median transaction lands in exactly the next slot (1 slot, ~120ms) —
completely healthy. But a real, substantial tail of transactions **miss
dozens of consecutive leader opportunities** before landing: up to 73 slots
(~20 seconds) in this run. That tail is what drives the "much slower than
expected" observation — not the read/notification path, whose own p99
(2.4s) is an order of magnitude smaller than write delay's p99 (19.4s).

### Read delay by lane

Unlike write delay, read delay *is* legitimately lane-specific — it
measures how long each individual channel took to notice an
already-landed transaction, so a genuine per-lane difference here reflects
something real about that channel's own delivery behavior:

| Lane | n | Read p50 | Read p99 |
|---|---|---|---|
| LowLatency (account) | 37 | 274.0 ms | 438.1 ms |
| LowLatency (tx) | 43 | 299.4 ms | 5,750.2 ms |
| Commit | 20 | n/a* | n/a* |

`n/a*` — Commit's own read delay can't be split out for a genuine data
reason, not a conceptual one: since its slot data is unreliable (see
caveat below), there's no trustworthy write-delay value to subtract from
its total latency for those 20 rows. Only Commit's *total* (send→observed)
latency is known: n=20, mean=2,510.5 ms, p50=1,997.3 ms, p99=7,563.4 ms
(see the companion report, `NATIVE_TRANSFER_LATENCY_REPORT_2026-09-01.md`,
for total latency broken down across all three lanes).

Both LowLatency sources tell the same story independently: read delay is
tight and consistent for each of them on its own (LowLatency (account)'s
own p99 is under half a second) — it's the *write* side, reported once
above, that stretches into double-digit seconds at the tail.

### Why the original version of this table was wrong

An earlier version of this report additionally split *write* delay by
lane (LowLatency (account): p50 71.1ms/p99 12,688.6ms; LowLatency (tx):
p50 365.0ms/p99 19,476.7ms) and presented that as a real per-lane
difference. It wasn't. Write delay for a given transfer is a single,
well-defined number regardless of which lane later reads it; partitioning
those 80 write-delay values by "which lane happened to win the read race
for this transfer" is selecting on an outcome correlated with — but not
caused by — the very quantity being measured (heavier real-world network
load plausibly lengthens both write delay *and* how backlogged each read
channel gets, correlating the two without either causing the other). That
correlation is enough to make two selection-based subsets of the same
underlying write-delay population look different by chance, especially at
n=37/43 with a heavy-tailed distribution where a couple of outliers
dominate the p99. The single pooled write-delay row in the table above
(n=80) is the only number that actually answers "how long does this test's
write path take" — it doesn't have a lane-specific counterpart because the
write path itself doesn't branch by lane.

## Caveat: the Commit lane's slot data is unreliable for this measurement

All **20 of 20** Commit-lane (rooted) samples in this run showed an
inclusion slot *earlier* than their own send slot — logically impossible
for a transaction to land before it was sent. This is 100% consistent
across every Commit sample, not an occasional glitch, which points to the
Commit lane's `header.slot` reflecting the periodic root-commit batch's own
slot number rather than that specific account's true last-write slot (root
finalization lags real time by design, ~12s in this codebase's own earlier
measurements). LowLatency (account) and LowLatency (tx) were completely
clean: 0 anomalies across 80 samples.

The code itself handled this safely — no crash, the write/read split just
comes back unresolved (`n/a` in the table below) for those rows, and the
20 Commit rows are excluded from the aggregate write/read percentiles
above. Their total (send→observed) latency is still real and valid; only
the write/read *split specifically* is unavailable for them.

## Full per-transfer data

`n/a*` = Commit-lane row; inclusion slot/write/read split unavailable for the reason above. Total latency is still valid for these rows.

| # | Signature | Lane | Send slot | Inclusion slot | Slots | Write (ms) | Read (ms) | Total (ms) |
|---|---|---|---|---|---|---|---|---|
| 1 | `33EMrwFU7cGQSsrGwoJLSWHrpwuq9SorxhbicAL3qxc1QZhVCrTQMd4xa3YZy9G6hjVRjg9hThDKqcKYkt83gx7K` | LowLatency (account) | 443335186 | 443335220 | 34 | 9974.1 | 298.9 | 10273.0 |
| 2 | `4A2ruajJvxpVN92tpQx9uqAR1YL1LLsKpv5g2T8EySwBVymNm7r5z17qmR3Nr2BDWaG38Z1TMsuYTrUfbmTS9dz5` | LowLatency (tx) | 443335220 | 443335224 | 4 | 1078.7 | 187.5 | 1266.3 |
| 3 | `3hVhs6YEzg9o1jpjbKXYWDLP52A11f6nzjsbAg3Wzy2QpEB5NQsVgqeFUMnsf9jJS4bnmDvf6G2AdD8HVGKqEX4P` | LowLatency (account) | 443335224 | 443335232 | 8 | 2300.3 | 230.1 | 2530.3 |
| 4 | `2HekZxLwCHXDfs3VNFcbrwp7w2fabVj62U8EzQcAynUx3BtNd71E8X1PLeDGhmBeBC6LdrGASmCnYrBcbadgvYn5` | LowLatency (tx) | 443335232 | 443335233 | 1 | 49.7 | 533.5 | 583.1 |
| 5 | `5Uxv7T5X5rBRSju1upmcuP5GksrnTBGWypQwjdtjmAMuf6cDo9JYWCCbCgb53pLdWTWcTPe2L5imbMwdu6byZiKn` | LowLatency (account) | 443335234 | 443335235 | 1 | 145.5 | 233.4 | 378.9 |
| 6 | `4JKKqYnqey23MAvi9tXddxY7szEWq2Ts2BAMmck4FZuZ1hS6SaACtzuNpqqGAKzwx6JHcMvtNcJgDNtxFD3eTYPN` | Commit | 443335235 | n/a* | n/a* | n/a* | n/a* | 6867.9 |
| 7 | `4BTeZ9poj6jvVDYtrjTJxFZjiSdovtiLrv1kuAhNVynqxqSpdBL4CrGCFAiJjqsUe8WxxpTrtrCbAGfAoCYwe7gE` | Commit | 443335257 | n/a* | n/a* | n/a* | n/a* | 2469.5 |
| 8 | `5eBL6pFBmYMQS1f9Q611dxSBWiMkpxMuo4LDys84h1hpvZXuLRL1xY7FcARPRC19ufMvTaZ2xQHXbwjHGMQtmS3b` | LowLatency (tx) | 443335265 | 443335272 | 7 | 2043.2 | 234.5 | 2277.7 |
| 9 | `4QTXEb1Dd4b2qLUCkjog2umJof3EGKapSSZxtkD9rACGmvKPCJSa2cEo3LNsKoQGBxYiKeVYALjN9KT358eHbYeS` | LowLatency (account) | 443335272 | 443335273 | 1 | 130.5 | 148.3 | 278.8 |
| 10 | `5FZH1ZHt5jJsdeTeQ6cJSFEegWXNMgpZWJ5ensxihFhdwtxm3SvwgsLSABVHna6RDw23JKSKYfWZmz2U36cfngzX` | LowLatency (tx) | 443335273 | 443335273 | 0 | 0.0 | 267.6 | 267.6 |
| 11 | `3czJk11pLEV4dyf1mHTM5mtZbcoi2BKW5vZ3RhxAKdMfzxGMtgSgbgPL2gxcvTNRp2nm8gBaKX2L9rvRz4ov2VH7` | LowLatency (account) | 443335274 | 443335280 | 6 | 1752.4 | 192.5 | 1944.9 |
| 12 | `4PNAMtYFwopVawAyhTfgK4QZZDECMAo54dSovJV15dDzMGnASK6WTGGUAfqMZaV3ucVEVCnnFds2FhcWpFoitx9n` | LowLatency (tx) | 443335280 | 443335281 | 1 | 39.7 | 401.7 | 441.4 |
| 13 | `4KzHGG4anFo3yprAFJCj9ZRerSCji64GaefwLi5vYtMYrdzqdjB9FBhGNLE7QVmiGCc4VXUwW8sEFkBY2CTvRaXG` | LowLatency (account) | 443335282 | 443335283 | 1 | 177.5 | 348.5 | 526.1 |
| 14 | `2Jwtk4x2CcYiRuVhY1ryyoueEBfHHy1KYx5EQT3d2vMZqwdUTCLuBNmCTufZZBNg5NDiVFj73JF6RyoASiFWtVkN` | LowLatency (tx) | 443335283 | 443335288 | 5 | 1313.7 | 363.2 | 1677.0 |
| 15 | `4JjU2zhUfZL6hJr6aSfCjt7GUe7ksgfxzvtHw9h5p1zTs449H8M58qqJitvz9cVxL9Hs4zY2ianK8KBrY8QAB21h` | LowLatency (account) | 443335289 | 443335290 | 1 | 208.1 | 305.5 | 513.6 |
| 16 | `27ctSGY4c1v3Ff3JToSL5Npw6ypsYftUULkpPqwdfFsXA5Bu4XBS1zHUThkddd8e56yfeNLyY3aBbEbkWoW8jvsc` | LowLatency (tx) | 443335290 | 443335291 | 1 | 0.1 | 346.8 | 347.0 |
| 17 | `2ix8wVXfi2XA35hvt9vkCbq1fmxFFKKeMdkSJJixVBj9u9qo8MUA5ydawXtLRqvjV4MEvtKgSyiBzbQzvp5rrTkg` | LowLatency (account) | 443335291 | 443335296 | 5 | 1128.0 | 329.2 | 1457.2 |
| 18 | `4GwR63y8n9AepTmp9N3maUVApzJWe4po5CAwtZLaZkuTHVQnrmkMvxnL9eoQJTMvoSqtvUr3BCioPAz6ou1jmNVy` | LowLatency (tx) | 443335297 | 443335297 | 0 | 0.0 | 300.1 | 300.1 |
| 19 | `4TVKCsttFjpsuKZWhCaH7bN1DH5RBXyBgAr3pd3VLmGJMcmc2n6hq6HT6LmUu8DdfaSLoXxBVEB5zTTJgvA4uYzC` | Commit | 443335297 | n/a* | n/a* | n/a* | n/a* | 2556.9 |
| 20 | `65NMZ2HhbaqAqekpAkZp6LMo9DG529QSdkqPp1KLD3piFuvwC1yGHeuQhHgGbr4udgXXBjo5jg39gATDnaDcmvQK` | LowLatency (tx) | 443335306 | 443335348 | 42 | 3801.9 | 9522.6 | 13324.5 |
| 21 | `3VAiGgaU4oMH9Fr3MMwV8pnXn7DdQGXn8xWNpMhaHQJLF2fAg2xA56q5Xq3KFVw6LWqTtcwLFUupiS2tAntTmuCR` | LowLatency (tx) | 443335319 | 443335392 | 73 | 13291.5 | 83.9 | 13375.5 |
| 22 | `2ay2M4khVsNH3F1PeBmpAVjzzTwZ5JHNEj8yw4d8jpckp628mXtNrUzDMK7GaixTeL6wroMu2VBGQVYj97HUw2Ue` | LowLatency (tx) | 443335392 | 443335444 | 52 | 16632.8 | 120.2 | 16752.9 |
| 23 | `5ww49dwqH1jz6rjAS2jBBqgTPWvenLfgBnzRWH8RtVPGzzvT9JdBKPfUN7Lxpgao3nFhoR8uDZtZN5CKFrncVtyz` | LowLatency (account) | 443335437 | 443335472 | 35 | 8381.9 | 263.7 | 8645.6 |
| 24 | `2yGPN5tnkwcQGKh8CH3gvXu2FziTx5ShrkRbRSXkmN1ckg1Aj8QjroEfVRgqA2xLBHEbReyU5Uyz24xJrmgDUeTN` | LowLatency (tx) | 443335472 | 443335473 | 1 | 80.9 | 319.4 | 400.3 |
| 25 | `FGQmzMQkEDhoB4f58tEWnDJxJVTMnCv9XCuwfJeD7h5Hh2Jt6kkVgv4aa94wUmy3p6BrDH65inVatYVKrMjkxvm` | LowLatency (account) | 443335473 | 443335474 | 1 | 0.1 | 163.1 | 163.2 |
| 26 | `VHG3g1bBHbbmNNmibrXK4nZ7GcdNGkVTxPV26bFDWo6FemWRHZowApzRAW67Ei55zLWLETR2MToKt14CS4ZP4RQ` | LowLatency (tx) | 443335474 | 443335474 | 0 | 0.0 | 230.7 | 230.7 |
| 27 | `562jufFFXTgdm5CnanVWew92ZCWC39aMvPW4xAr3QZRukBBxJhbcMvh1xVKdXsDTGHgmxjLLxsJt4N34UAWaxsC2` | LowLatency (account) | 443335475 | 443335475 | 0 | 0.0 | 279.3 | 279.3 |
| 28 | `iEAfDiQdST74AXUiYC1y2aQmPEhSX6eiDyCbdjs9T13rBrLeX1BZQYQPR67aXVv6hQDnxZy8TwAei45hd8v2L7p` | Commit | 443335476 | n/a* | n/a* | n/a* | n/a* | 280.5 |
| 29 | `5NyczGYUiwEgCPXSwzqWY3CZH5HpLjdsd8kQk25KN56cQo6dUdY3LJuVCjyxG7smYrTkr3odYGWTAPx884nkFC1w` | LowLatency (account) | 443335477 | 443335477 | 0 | 0.0 | 273.5 | 273.5 |
| 30 | `317m5VVy8scLbm6pS2u98QmejFZ2iWXZ3XtEXvmNw4wwqFxu6is5aRjvs126CzsGqwQJ3HryxpsYLAwAUyEyU8ay` | LowLatency (tx) | 443335477 | 443335478 | 1 | 0.2 | 291.1 | 291.3 |
| 31 | `3fPRgvmMv2fvDqJWZUvwnTPiQsb8W7Ljr7q1N6AbokE3fFu61JXkWzdLzLqVLer6eDa8M6BMoTuAAybV21vyyFKc` | LowLatency (account) | 443335478 | 443335479 | 1 | 0.1 | 254.3 | 254.4 |
| 32 | `525tjMXKvfvnYeCJPuGypUtpmmqiMv9jxQ9FbVkuQqwhDKwydYFZfpYmzL56axUrcHwdQKiNZvWYbmtKk6D3JkJM` | LowLatency (tx) | 443335479 | 443335484 | 5 | 1287.9 | 300.4 | 1588.3 |
| 33 | `29px25z6nemszCSZWiPkcbnWuhvP8aRy84GqGKm5v93WgggWy6XuifNmB1QYXGTSMzsz3wSZmo35H7FvizjVD2UX` | Commit | 443335484 | n/a* | n/a* | n/a* | n/a* | 6019.4 |
| 34 | `3CFHLVWpsgMx2Q7Wt9FN1EcTDpHZUYMxVwWaVGqtJ7AyfH5TXfg6429m4uEz9VqmLQigBbEkzVDnG94MDjv56ngz` | LowLatency (tx) | 443335505 | 443335512 | 7 | 1956.8 | 265.4 | 2222.1 |
| 35 | `3REwvwbiJpUKuSbQdTFMdPdEqUQxNJVajYnqnF8pKd24vXAbwPa4XkpfwJVYyiwEhT49ygk6W6jswiy9KdmCydcP` | LowLatency (account) | 443335512 | 443335540 | 28 | 8256.2 | 250.1 | 8506.3 |
| 36 | `1Kc4vDS9WfWK3KK9qUPw37rrzHhiWSXZeiWyuh4qgTVnAUj15vY8uTQ6s1fmj5Mgpv5MoLc6zjRdfjwNpwDAfsw` | Commit | 443335540 | n/a* | n/a* | n/a* | n/a* | 1319.2 |
| 37 | `3bpNABMG19hoxCYr4mxSQFW7Sf5Umjp4AfKf1chtpRBELavXEaxAYmxhPKzD6wc4qAFxwG8nrMFoJy2oDB4cqAy3` | LowLatency (account) | 443335544 | 443335548 | 4 | 987.0 | 196.8 | 1183.8 |
| 38 | `HZk51xetzC9SPDmgUbN2HHfU1mtkGfuvgZuQnzycRm8Qd23XK5b6Bq6yucLwBo4HmwrAjkoXkKC7ywSqFDbBfcB` | LowLatency (tx) | 443335548 | 443335549 | 1 | 96.7 | 244.3 | 341.1 |
| 39 | `2gfTWke2jS4EvWCVgLUcctR8N85w62k7SP9cxEU1qZ7U2xNvAachB7EYJqxcdgUSw7ZnADN8wFrnpGYZGENRkid4` | LowLatency (account) | 443335549 | 443335550 | 1 | 61.7 | 199.2 | 260.9 |
| 40 | `3XNP6gAdbv3jpUCtncfMu4jxbykQeonPNxAKZZwRVq7M3NgHBsTuAa492uD7CJoqFitnSR3DRF7CnoZ49Rjn9ySk` | LowLatency (tx) | 443335550 | 443335556 | 6 | 1632.7 | 167.4 | 1800.1 |
| 41 | `RMQerLaVGgx8VuhkjURaoo66nGTZS7nkmTQrbGiFV2EwRKvxgp46H3JBb885sXZHqu8CQqsnpfuyZiqY6V2BLx8` | LowLatency (account) | 443335556 | 443335557 | 1 | 124.0 | 231.7 | 355.7 |
| 42 | `3DVtVw8q1tZdQwEGgrQyAJ38PhGS1N1cHq9r8HQiNTexM8DC4KeSppuk4rB5LebAWVRdap3LYw46epLn4WTYcf2C` | LowLatency (tx) | 443335557 | 443335558 | 1 | 76.8 | 254.6 | 331.4 |
| 43 | `5CFq5F974GVyVzek6UTqz6YZEKbaRqAa65FYrjG7ED6JZyyRiNkF1UwqRknW3w7peRkxzJNy1xe6kUickHGF6FLs` | LowLatency (account) | 443335558 | 443335559 | 1 | 51.3 | 347.6 | 398.8 |
| 44 | `dHcKJbKBzGhN7sMU7m9vVoqXmNkAbBQjvrUZmfuPUA4Js2EASEuSM2npm7LYexhVCQK8tSXDpmAmWd1ncnnhy4E` | LowLatency (tx) | 443335559 | 443335560 | 1 | 2.2 | 296.3 | 298.5 |
| 45 | `4w72LmEeB4LKRREZLMGyFVi9S5bkMwM7ePYovKvvCtDpU9nezJZxxEYvmdHGTuxLAezYRoreo9qrNUnE9Dqrx7j1` | LowLatency (account) | 443335560 | 443335560 | 0 | 0.0 | 274.0 | 274.0 |
| 46 | `2TmNiH7FrRhaVcoVWJh5gT9JJwRbzviUC3btUr8CRiZPoX5y5kpy9S7kjsVSqXhJ3Cus8LQkjXZgLMJEmVf162s9` | LowLatency (tx) | 443335560 | 443335561 | 1 | 0.2 | 333.4 | 333.6 |
| 47 | `3iVy69M2DfpW8MmD96YaqjZSAGW3yU2sL4RXyvTvrSVPuo3DgcWa1AjNicU4Wwj4jYYX5dncR82byq93T2XoQW7d` | Commit | 443335561 | n/a* | n/a* | n/a* | n/a* | 4281.2 |
| 48 | `4U1F4YU9ohTU1uKFS6pqsPPGJnSeVnLhM6sEHbiQ7whvbcDfdzXd7qvH7tmWosGjj3PL5sPmreo7cEzN9eVGtWL1` | LowLatency (tx) | 443335572 | 443335575 | 3 | 628.3 | 540.6 | 1168.9 |
| 49 | `2wQMJPBTLKKy9szxoxBXoENhhrGHPGLcNeSXFFQvQHo3Aepmdhj2FEKDt99HgGi32sqR6oDnwc8Az5hs3udNXksi` | LowLatency (account) | 443335575 | 443335580 | 5 | 1022.0 | 210.4 | 1232.3 |
| 50 | `325b7Ji8HXPHnABb5mSsveQKJb3pWtf2TNEYiGihksyQjptV7mAEv7XoTdsTLSYG6BBb616sopujxsT3GDYP3jM7` | Commit | 443335580 | n/a* | n/a* | n/a* | n/a* | 16.3 |
| 51 | `4jUQyv3RetNw7S51tqpx8GxpChXf9wL1qA1WgnsriyRrxZJYVnRXRFq6y3ACoYk7hoZtw8BmF6xsPRqHbB79jFHT` | Commit | 443335580 | n/a* | n/a* | n/a* | n/a* | 585.8 |
| 52 | `CzU5AQZAaDNtKU8Pqx7snJyZ26ryidbJqg6UFNs43prWZDcKodQemuhUrTScDzjRb4mhHRku4EFRhM5TuJduXpe` | Commit | 443335582 | n/a* | n/a* | n/a* | n/a* | 7726.6 |
| 53 | `cHe8374DTbn4V7wFgfZABsCyyvVxnQCmFZKPGPLWXcWo3EMWyYb5VVfFD4gPH1wcJxWjSYped8CohR8SWz36yNW` | Commit | 443335608 | n/a* | n/a* | n/a* | n/a* | 1456.3 |
| 54 | `35QiqZtyiuasXAVbPXCXGQYyaSKv4DF4XGgViu4jbg9xoLoJQHpsXsUadJo9LhhLL4YynsDNqcpugP2KfWfxTEMd` | LowLatency (tx) | 443335612 | 443335676 | 64 | 19825.2 | 201.1 | 20026.2 |
| 55 | `67BQiiH1hiVnzSiXAW27nznMPM51Bj3zda354DGKgZxgNZ2J24juuSyKvSXCdLmnBmBbYMRLNeCqCyxo7u3Z7vQj` | LowLatency (account) | 443335676 | 443335676 | 0 | 0.0 | 352.1 | 352.1 |
| 56 | `qE2eMamyU1yPdzYunMWAGxNp2yJCXjwQNRHoVSgQmNLEeFAj9iSSMGyiStvb8RNPGyjzcYepJdA8WmcVSmZwAMK` | LowLatency (tx) | 443335677 | 443335678 | 1 | 130.9 | 287.2 | 418.1 |
| 57 | `4CztVi8iWnwN5r915ATSBg2BrCgQKPSCXVYxtZZbGEb61LRqRfNw4Qad1SuLMp8vqkXzJ7HVPk9qd4zoFasi3YBG` | LowLatency (account) | 443335678 | 443335679 | 1 | 0.1 | 224.3 | 224.5 |
| 58 | `3ZpjGtDmEEbLz7KETUY8Hje5ZywbcyWFtPkK4A148ca9krErRGYXunCifEymTzb8j1YainN1Yw7dkGotQ4QHeUu8` | LowLatency (tx) | 443335679 | 443335684 | 5 | 1294.4 | 341.0 | 1635.4 |
| 59 | `4CZepHb9cGddBhpQM9yWLkutC1b5uNt88RaP8RyJrfpAYkYcNhzKTWWpnPRmvzmPjyrPkLt1akFTKcKp9y368t1q` | LowLatency (account) | 443335684 | 443335685 | 1 | 2.1 | 395.8 | 397.9 |
| 60 | `5XRi4NSmN35EZ4BG9NBWHKP6rV7bkWNjqp8VFwSV65aN3hcghkH78w5x91Jw1jsKLUbBkJqDaVphVbKstJxe15EN` | LowLatency (tx) | 443335686 | 443335686 | 0 | 0.0 | 335.4 | 335.4 |
| 61 | `CbqYPG2ubwb1SRw3oWZ7iVwCdXfoHiQg8y5u2tURW6Aai8F7HZUb3BX3AQhkarnNYr2AAwHBSE5dcAeZYcNcu6g` | LowLatency (account) | 443335687 | 443335687 | 0 | 0.0 | 207.2 | 207.2 |
| 62 | `2CbJxVZW98VMk8Pm9pMSEB8vzPoTiqNnuaozm2e7PdPEfAN5M18cMoGuHu4fQa7KoLDs3iWRHLwXRNY316PqUgi8` | LowLatency (tx) | 443335688 | 443335690 | 2 | 502.3 | 413.7 | 916.0 |
| 63 | `4gpm2FKWB3eT1JwGBLpWUEdNf5X7XxavRbpb1Q8UXjwERzXHf9DnTgzdMWxYvo5WVUJxcRpfA7Tm9cskxGRXwwpv` | LowLatency (account) | 443335691 | 443335691 | 0 | 0.0 | 343.3 | 343.3 |
| 64 | `21C73TxudyFokC2BgttmojoQhsU8CFDh8UhAN1A1dkwn4AUZMUetzE8F6kT1JD67aQHZpKoiVUFTS4wL9WscL8LL` | Commit | 443335692 | n/a* | n/a* | n/a* | n/a* | 3013.8 |
| 65 | `4T4LBLQudZVCtZFvhQXKm9Ai1NyMSc5Rv8chnfaU9JDxqBnCw5vKbrzytY9mtbszW6PopgFk3hpEfza611iDDybz` | Commit | 443335701 | n/a* | n/a* | n/a* | n/a* | 3260.6 |
| 66 | `35WGr296tcZ8UTqfasrDvL8jEkmmSn8WvFWEqAiCzMEshu2MycKe659aieLN1qY6A39DpFydrSXMZGRubgnmocUu` | LowLatency (tx) | 443335712 | 443335772 | 60 | 18995.6 | 288.1 | 19283.7 |
| 67 | `5ZQwKCW9mq3obKREMeXzPgscHUQLVgwMZmMFU2vPDgxPoaC9EcnXD4dGNDRHCNPox6jy6XYBfTXXNKyAhVmFmSUg` | LowLatency (account) | 443335772 | 443335773 | 1 | 65.4 | 437.0 | 502.4 |
| 68 | `55UVfjHZy9kvjBJSzMjnU11odV5nYbagbrZkh9FbqtJFWzZMsw2iJbDWqH9MmmP5ZYXXGMUUDLHc62M8bE97F25n` | LowLatency (tx) | 443335774 | 443335792 | 18 | 5167.3 | 359.8 | 5527.1 |
| 69 | `2AvwiXFTis5ApbTxgRpPS3YfikvEgNgA6aVyhEdiPdciN2igAYtGHksje2ffPrjg9fni5Z3Q3yeWLaqbJyHBPcxS` | LowLatency (account) | 443335792 | 443335794 | 2 | 261.4 | 342.0 | 603.3 |
| 70 | `31iye7V3uyZdx8FewTeDiesSRnQhDKV45kX3XnVrFZWa9BzU2AXGySDdDehM7phgDzRPfQFu1J9z6n6j5NmYC8Cg` | LowLatency (tx) | 443335794 | 443335795 | 1 | 5.3 | 310.0 | 315.3 |
| 71 | `2gzsgDLkLZ9rPJSJFiL95tVFTCVXz4B8phNzABrhee3CtGPfZFRQDtw3tAcAo3uLYiusFPGnHpJ3o9mAdekFUFbf` | Commit | 443335795 | n/a* | n/a* | n/a* | n/a* | 3160.6 |
| 72 | `66QA1w19y4NEzWzMvCiJHn1fPK2UhQ733oSMMz1zGRaBHjNXSVP7XyHXtc4tJogqTcrsumREjeu9AaZx9rdifSKj` | LowLatency (tx) | 443335806 | 443335824 | 18 | 5952.3 | 299.4 | 6251.7 |
| 73 | `33CXBpLAX2s6Xnh5qKwoP53SLjL2y5oAe2yzqYUC8Fbs7FG3ZisSFMDWPJF8PMVYffq2dZt5s3uSrGhNS8M2giZW` | LowLatency (account) | 443335824 | 443335825 | 1 | 0.1 | 438.6 | 438.7 |
| 74 | `5KWC8hiMKu6U3pHXJkYqNo5ti1AcPuudNbR9B93imqPcyCXrKdUczfNvP8ufFPp8hNhitQvHvT6bd4YgietUq9PY` | LowLatency (tx) | 443335826 | 443335826 | 0 | 0.0 | 314.6 | 314.6 |
| 75 | `bqQJZ9qXWSa75HEFWwucajANAuAWAExCcAMFQX2qBUWhz62TZHxq7EYmsQN7BQPxJnPEJJMjHJByQ3mRYibw2z7` | Commit | 443335827 | n/a* | n/a* | n/a* | n/a* | 5665.6 |
| 76 | `2q5Z7WKJGQ6c8BXv7M2GAGU9hF6zY8nxWLAEvFdcjdbKPs1cVFPqfvHe2NMHNzJT4cTyHwFtm9xdd84EZi7iUjZP` | LowLatency (tx) | 443335845 | 443335856 | 11 | 3503.3 | 250.7 | 3754.0 |
| 77 | `3M4jtP7FezFzt28xdBLR3mbKsf7Cj6xDctLZzoaU3XDBxzeB7Vhzna7dkZvR1UMzDMdsetdziiNUEQxk9r5w2a5p` | LowLatency (account) | 443335856 | 443335857 | 1 | 116.9 | 194.6 | 311.5 |
| 78 | `2tiEAeT1sVvXyyuEYzYoR1B6RmVi1nzEKRCCZzWm8j5x31vrnh14mvuyavdTGfXkpZqavF9NDrk3bLzcenmqm4sc` | LowLatency (tx) | 443335857 | 443335858 | 1 | 47.2 | 260.1 | 307.3 |
| 79 | `3yXiHF1LbfP7YA2cdiz2badpZukhb8RU1CRLF6UtyC1es4Escfiz5F3Qt3cjuT77v7wdd4b84njhMExeR4trsbL` | LowLatency (account) | 443335858 | 443335859 | 1 | 42.7 | 214.6 | 257.3 |
| 80 | `3bMFq2PXteyM2Esx4HKyiUWaEgo2n8MVKiNB2pUYpfk7NMQM4Qjrinw1fZrsxiVqJnAYxAHcbZCujDmM4QxJRnQK` | LowLatency (tx) | 443335859 | 443335875 | 16 | 5289.2 | 300.8 | 5589.9 |
| 81 | `2km9vJJf1FkQQwKj4zHpRDFpFTAEjdsAc5AMMf67xSxXtfGV164Drw43eoG22hfeqporeV55Zuy4j4Fx7bxRMdii` | Commit | 443335875 | n/a* | n/a* | n/a* | n/a* | 1525.1 |
| 82 | `53sQNdzKVtXLhBPuGYisdFadveiwcqxZmJNLWKZm4B4gnxiuh5ejjau5f6CntL9qAsghZppWhk3af2RYV1LPvR61` | LowLatency (account) | 443335880 | 443335924 | 44 | 14215.5 | 317.1 | 14532.7 |
| 83 | `Cz6bg6vG4coiKu291Neg8Syo2h6B8m3pJKoLzQJqezkXonkrTZSHvpf1qn6s6a4HbHmqWjqw21SnKRE7Kmw6jc7` | LowLatency (account) | 443335924 | 443335956 | 32 | 9931.2 | 397.0 | 10328.2 |
| 84 | `38AvJGeSEm1mLJucetZfJaZR8C67j542phtsMjT1NLyQjQDQ9bxrX2wNr78RbSt4peWe5S53qodZNKvB83akkV5j` | Commit | 443335956 | n/a* | n/a* | n/a* | n/a* | 1.2 |
| 85 | `53uGBfijuMMAhGHB2hwQpQ17QMRFMqBzAT6YXGfBykDhF56U43qB6WyKYd2y8s9xzDUZDJUbpLwuVZY8LWV5bMsF` | LowLatency (account) | 443335957 | 443335957 | 0 | 0.0 | 414.9 | 414.9 |
| 86 | `5ywNM4A5fHBi76wuSjKGeMkRmV7G6GD1rAEUnZbU5d8Nwizj9Rc5RM2exbwbue2PxLJRK2ekwWjiHgz4X5bxfPrt` | LowLatency (tx) | 443335958 | 443335960 | 2 | 455.5 | 345.1 | 800.6 |
| 87 | `Cs5q3srWUBzRsNWwLxmYG5Bi4LZ2jGmyW8wrQZT6VQaJzPWjjJoTy29yAkzA72WtRVHzvtYkvJWoYrNV67FaF9t` | LowLatency (account) | 443335960 | 443335961 | 1 | 2.9 | 337.8 | 340.7 |
| 88 | `3NcX3kfPd7q86iQqXkPaNUXk3x3nVXdyFjL2jdmuowsVa8UcxowkLDdUyCtTox523kEJXTMmMib9YQeAnNcrjGCt` | LowLatency (tx) | 443335962 | 443335962 | 0 | 0.0 | 281.4 | 281.4 |
| 89 | `5RqhRo9h68z3qtY8ZVFsHa8ShiFrHhzJnLLneP8pvFVyJzZZah4HJyrpdmHKL2sioQyKn42eiaGNmM6PAAK76666` | LowLatency (account) | 443335963 | 443335972 | 9 | 2705.7 | 265.0 | 2970.7 |
| 90 | `3kjvmpwKxrgnw8rYmKF9wuxzZA8APqFrzuQnT4Q8PSfXbufGkhWrBGEsj9gaA1913z9yTrzJRivbUMZAFp9ApKQ` | LowLatency (tx) | 443335972 | 443335974 | 2 | 365.0 | 303.4 | 668.4 |
| 91 | `52Z5cn8CehLWqMfdHbUqQ5niVDW9NGcL8b4ZL7zSWw12TCULF1o2nHm883phVnrkXyqbjGprXQMskv4vUqY9oHUS` | LowLatency (account) | 443335974 | 443335975 | 1 | 2.8 | 381.3 | 384.1 |
| 92 | `2dy7JeffPgWE464s4VDmuyd4Mdq1mCZfAkepnqGVBNHDJrNn7srsmJbEtUyNgqQx9HHGrJYcxeSjVEsTqkBPgiu1` | LowLatency (tx) | 443335975 | 443335988 | 13 | 3724.9 | 236.0 | 3960.9 |
| 93 | `5s5yLA8WXhgebBRGzu7bhTeGsw8E6KyEMUWg91faNAVWUSsxXS3UTAjv9n5UvVVRA1qhx82epAak6BfybkFCQPUA` | Commit | 443335988 | n/a* | n/a* | n/a* | n/a* | 1.6 |
| 94 | `5HYjzcfqLTNz88UgEbDRKVKQWarubpJN3Wy1sBmUXnX2o2PEE73MgdJsZeec3d1sMG1jxgcdmsnhvAWnDHwK1uuL` | LowLatency (tx) | 443335988 | 443335989 | 1 | 70.1 | 231.9 | 302.0 |
| 95 | `4qHtX8QoN4FRmuqnof7UKJaAe7CPstzCzbAqMbh9pR5thXXUdWBv2hBntZeGbi6woMx6ymT1nh3FuY85ppvAe316` | LowLatency (account) | 443335989 | 443335990 | 1 | 71.1 | 337.7 | 408.8 |
| 96 | `XxnJpR3nbBsZvByKjBnWJoEo6fMgzQDuNvm2ZzAw87E2M9sQFfG158NUPDXWFAuE8EunuTaVUjFqi2fSAoqFgbe` | LowLatency (tx) | 443335990 | 443335991 | 1 | 0.8 | 329.3 | 330.1 |
| 97 | `2Y5z9vH7HSm9TWdyFaFFQ3T5c9En12BEiM7wadoxW7kE475Xmk3Qy4WuRBj7GAmuq1u5jWxYJvVgrsEAqgCWYkHF` | Commit | 443335991 | n/a* | n/a* | n/a* | n/a* | 1.1 |
| 98 | `3bCR3uAx61RPjsnjykvkSQ65KXcF8LphxBXgEKQJSBGXH9EQcJs7dfdXJGomKCBj1qRtivTTbhB4KDUoMhgvvv3m` | LowLatency (tx) | 443335991 | 443336004 | 13 | 3724.7 | 236.2 | 3960.8 |
| 99 | `41c8rrpqeiDc5JqazdjGQGBAwX2e2qkxGqga4qGJooNhBhwkNSr2ZUv1J3MytTV8SsdS1UE7z5vvbo7zcYHVZmHx` | Commit | 443336004 | n/a* | n/a* | n/a* | n/a* | 0.5 |
| 100 | `5Q9gxUibfXTqx2dhU8Ni2cnN4Qh8nwoi4QqWtazSY53ZS23iZmXYaom7bxGv97ZzLm3BcLKX8spBPR6MRkNtYP16` | LowLatency (tx) | 443336004 | 443336005 | 1 | 35.6 | 350.3 | 386.0 |

## What this means for the "much slower than expected" observation

The read path (whichever of LowLatency (account) / LowLatency (tx) / Commit
wins) is fast and consistent — its own worst case across 80 clean samples
is 5.75 seconds, and its p50 sits around 275-300ms regardless of which
source won. The real variance driving the overall loop's slowness is
almost entirely on the
write side: roughly 1 in 5 transfers in this run needed more than 10 slots
(~3+ seconds) just to land, and the single worst case needed 73 slots
(~20 seconds) before the transaction was even included on-chain. Any
further latency-reduction work should target *why* transactions
occasionally miss many consecutive leader opportunities (fee/priority
strategy, RPC/relay routing, network congestion at send time), not the
account-update or transaction-confirmation delivery paths -- those are
already fast.
