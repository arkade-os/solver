CREATE TABLE IF NOT EXISTS preimage_claim (
    pk_script BLOB PRIMARY KEY,
    claim_address TEXT NOT NULL,
    preimage BLOB NOT NULL,
    arkade_script BLOB NOT NULL,
    taptree BLOB NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_preimage_claim_created_at ON preimage_claim (created_at DESC);
