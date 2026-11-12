CREATE TABLE checkouts (
    id                 text PRIMARY KEY,
    amount_base_units  numeric(78,0) NOT NULL CHECK (amount_base_units > 0),
    merchant           bytea NOT NULL CHECK (octet_length(merchant) = 20),
    token              bytea NOT NULL CHECK (octet_length(token) = 20),
    chain_id           bigint NOT NULL,
    -- EIP-3009 authorization nonce; one checkout per authorization.
    nonce              bytea NOT NULL UNIQUE CHECK (octet_length(nonce) = 32),
    valid_after        bigint NOT NULL,
    valid_before       bigint NOT NULL,
    merchant_reference text NOT NULL DEFAULT '',
    status             text NOT NULL CHECK (status IN
                         ('pending', 'submitting', 'submitted', 'confirmed', 'failed', 'expired')),
    payer              bytea CHECK (octet_length(payer) = 20),
    signature          bytea,
    tx_hash            bytea CHECK (octet_length(tx_hash) = 32),
    failure_reason     text NOT NULL DEFAULT '',
    confirmed_block    bigint,
    relayer_nonce      bigint,
    created_at         timestamptz NOT NULL,
    expires_at         timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL
);

-- The reconciler scans non-terminal rows by status.
CREATE INDEX checkouts_status_idx ON checkouts (status);
