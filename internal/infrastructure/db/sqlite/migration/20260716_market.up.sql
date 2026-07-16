DROP TABLE IF EXISTS swap_pair;

CREATE TABLE market (
    base_asset TEXT NOT NULL,
    quote_asset TEXT NOT NULL,
    base_decimals INTEGER NOT NULL DEFAULT 0,
    quote_decimals INTEGER NOT NULL DEFAULT 0,
    min_quote_amount INTEGER NOT NULL DEFAULT 0,
    max_quote_amount INTEGER NOT NULL DEFAULT 0,
    min_base_amount INTEGER NOT NULL DEFAULT 0,
    max_base_amount INTEGER NOT NULL DEFAULT 0,
    price_feed TEXT NOT NULL,
    slippage_bps INTEGER NOT NULL DEFAULT 0,
    fee_bps INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (base_asset, quote_asset)
);

ALTER TABLE trade RENAME COLUMN pair TO market;
