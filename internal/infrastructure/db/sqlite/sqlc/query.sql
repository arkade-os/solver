-- name: InsertMarket :exec
INSERT INTO market (base_asset, quote_asset, base_decimals, quote_decimals, min_quote_amount, max_quote_amount, min_base_amount, max_base_amount, price_feed, price_path, slippage_bps, fee_bps, price_ttl_seconds)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateMarket :execrows
UPDATE market
SET base_decimals = ?, quote_decimals = ?, min_quote_amount = ?, max_quote_amount = ?, min_base_amount = ?, max_base_amount = ?, price_feed = ?, price_path = ?, slippage_bps = ?, fee_bps = ?, price_ttl_seconds = ?
WHERE base_asset = ? AND quote_asset = ?;

-- name: DeleteMarket :exec
DELETE FROM market WHERE base_asset = ? AND quote_asset = ?;

-- name: ListMarkets :many
SELECT * FROM market;

-- name: InsertTrade :exec
INSERT INTO trade (market, deposit_asset, deposit_amount, want_asset, want_amount, offer_txid, fulfill_txid, error, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListTrades :many
SELECT * FROM trade
WHERE (
  CAST(sqlc.arg(status) AS TEXT) = ''
  OR (CAST(sqlc.arg(status) AS TEXT) = 'failed' AND error != '')
  OR (CAST(sqlc.arg(status) AS TEXT) = 'succeeded' AND error = '')
)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(limit);
