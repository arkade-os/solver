-- Per-side asset display metadata for the discovery card, plus the feed's
-- decimal encoding (price = feed value / 10^price_decimals; 0 = feed returns
-- the price directly).
ALTER TABLE banco_pair ADD COLUMN base_name TEXT NOT NULL DEFAULT '';
ALTER TABLE banco_pair ADD COLUMN base_ticker TEXT NOT NULL DEFAULT '';
ALTER TABLE banco_pair ADD COLUMN quote_name TEXT NOT NULL DEFAULT '';
ALTER TABLE banco_pair ADD COLUMN quote_ticker TEXT NOT NULL DEFAULT '';
ALTER TABLE banco_pair ADD COLUMN price_decimals INTEGER NOT NULL DEFAULT 0;
