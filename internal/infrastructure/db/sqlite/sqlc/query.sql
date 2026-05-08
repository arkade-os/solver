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
