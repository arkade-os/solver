ALTER TABLE trade RENAME COLUMN market TO pair;

DROP TABLE IF EXISTS market;

CREATE TABLE swap_pair (
    pair TEXT PRIMARY KEY,
    min_amount INTEGER NOT NULL,
    max_amount INTEGER NOT NULL,
    base_decimals INTEGER NOT NULL DEFAULT 0,
    quote_decimals INTEGER NOT NULL DEFAULT 0,
    price_feed TEXT NOT NULL,
    invert_price INTEGER NOT NULL DEFAULT 0,
    slippage_bps INTEGER NOT NULL DEFAULT 0
);
