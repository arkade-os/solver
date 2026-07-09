ALTER TABLE banco_pair DROP COLUMN price_decimals;
ALTER TABLE banco_pair DROP COLUMN quote_ticker;
ALTER TABLE banco_pair DROP COLUMN quote_name;
ALTER TABLE banco_pair DROP COLUMN base_ticker;
ALTER TABLE banco_pair DROP COLUMN base_name;
ALTER TABLE banco_pair RENAME COLUMN max_base_amount TO max_amount;
ALTER TABLE banco_pair RENAME COLUMN min_base_amount TO min_amount;
ALTER TABLE banco_pair DROP COLUMN fee_bps;
ALTER TABLE banco_pair RENAME COLUMN tolerance_bps TO slippage_bps;
