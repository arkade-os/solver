-- Trade-size bounds move from the want (quote) side to the base side of the
-- trade, per the discovery protocol: min/max are expressed in base-asset
-- atomic units and enforced against the maker's deposit. Values are NOT
-- converted (that would require a price); operators must review configured
-- bounds after upgrading.
ALTER TABLE banco_pair RENAME COLUMN min_amount TO min_base_amount;
ALTER TABLE banco_pair RENAME COLUMN max_amount TO max_base_amount;
