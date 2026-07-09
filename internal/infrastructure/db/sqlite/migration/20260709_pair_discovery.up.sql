-- Discovery protocol v0 pair fields.
--
-- Vocabulary: the internal fill-time price band is the "tolerance" (never
-- published), the published spread is the "fee".
ALTER TABLE banco_pair RENAME COLUMN slippage_bps TO tolerance_bps;
ALTER TABLE banco_pair ADD COLUMN fee_bps INTEGER NOT NULL DEFAULT 0;

-- Trade-size bounds move from the want (quote) side to the base side of the
-- trade: min/max are expressed in base-asset atomic units and enforced
-- against the maker's deposit. Values are NOT converted (that would require
-- a price); operators must review configured bounds after upgrading.
ALTER TABLE banco_pair RENAME COLUMN min_amount TO min_base_amount;
ALTER TABLE banco_pair RENAME COLUMN max_amount TO max_base_amount;

-- Per-side asset display metadata for the discovery card, plus the feed's
-- decimal encoding (price = feed value / 10^price_decimals; 0 = feed returns
-- the price directly).
ALTER TABLE banco_pair ADD COLUMN base_name TEXT NOT NULL DEFAULT '';
ALTER TABLE banco_pair ADD COLUMN base_ticker TEXT NOT NULL DEFAULT '';
ALTER TABLE banco_pair ADD COLUMN quote_name TEXT NOT NULL DEFAULT '';
ALTER TABLE banco_pair ADD COLUMN quote_ticker TEXT NOT NULL DEFAULT '';
ALTER TABLE banco_pair ADD COLUMN price_decimals INTEGER NOT NULL DEFAULT 0;
