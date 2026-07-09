-- Discovery protocol vocabulary: the internal fill-time price band is the
-- "tolerance" (never published), the published spread is the "fee".
ALTER TABLE banco_pair RENAME COLUMN slippage_bps TO tolerance_bps;
ALTER TABLE banco_pair ADD COLUMN fee_bps INTEGER NOT NULL DEFAULT 0;
