ALTER TABLE banco_pair DROP COLUMN fee_bps;
ALTER TABLE banco_pair RENAME COLUMN tolerance_bps TO slippage_bps;
