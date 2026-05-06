-- name: InsertPair :exec
INSERT INTO banco_pair (pair, min_amount, max_amount, base_decimals, quote_decimals, price_feed, invert_price)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: UpdatePair :execrows
UPDATE banco_pair
SET min_amount = ?, max_amount = ?, base_decimals = ?, quote_decimals = ?, price_feed = ?, invert_price = ?
WHERE pair = ?;

-- name: DeletePair :exec
DELETE FROM banco_pair WHERE pair = ?;

-- name: ListPairs :many
SELECT * FROM banco_pair;

-- name: InsertTrade :exec
INSERT INTO trade (pair, deposit_asset, deposit_amount, want_asset, want_amount, offer_txid, fulfill_txid, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListTrades :many
SELECT * FROM trade ORDER BY created_at DESC, id DESC LIMIT ?;

-- name: InsertPreimageClaim :exec
INSERT INTO preimage_claim (pk_script, claim_address, preimage, arkade_script, taptree, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(pk_script) DO UPDATE SET
    claim_address = excluded.claim_address,
    preimage = excluded.preimage,
    arkade_script = excluded.arkade_script,
    taptree = excluded.taptree,
    created_at = excluded.created_at;

-- name: GetPreimageClaim :one
SELECT * FROM preimage_claim WHERE pk_script = ?;

-- name: DeletePreimageClaim :execrows
DELETE FROM preimage_claim WHERE pk_script = ?;

-- name: ListPreimageClaims :many
SELECT pk_script, claim_address FROM preimage_claim ORDER BY created_at DESC;

-- name: ListAllPreimageClaims :many
SELECT * FROM preimage_claim ORDER BY created_at DESC;
