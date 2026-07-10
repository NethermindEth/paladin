PRAGMA foreign_keys=off;

DROP INDEX IF EXISTS indexed_transaction_id;
DROP INDEX IF EXISTS indexed_transaction_from_nonce;
DROP INDEX IF EXISTS indexed_transaction_from_chain_nonce;
DROP INDEX IF EXISTS indexed_events_signature;
DROP INDEX IF EXISTS indexed_events_transaction_hash;

ALTER TABLE indexed_events RENAME TO indexed_events_old;
ALTER TABLE indexed_transactions RENAME TO indexed_transactions_old;

CREATE TABLE indexed_transactions (
    "hash"                    VARCHAR   NOT NULL,
    "block_number"            BIGINT    NOT NULL,
    "transaction_index"       BIGINT    NOT NULL,
    "from"                    CHAR(40),
    "from_chain"              TEXT,
    "to"                      CHAR(40),
    "to_chain"                TEXT,
    "nonce"                   BIGINT    NOT NULL,
    "contract_address"        CHAR(40),
    "contract_address_chain"  TEXT,
    "result"                  VARCHAR,
    PRIMARY KEY ("block_number", "transaction_index"),
    FOREIGN KEY ("block_number") REFERENCES indexed_blocks ("number") ON DELETE CASCADE
);
CREATE INDEX indexed_transaction_id ON indexed_transactions("hash");
CREATE UNIQUE INDEX indexed_transaction_from_chain_nonce ON indexed_transactions("from_chain","nonce");

INSERT INTO indexed_transactions (
    "hash", "block_number", "transaction_index", "from", "from_chain", "to", "to_chain", "nonce", "contract_address", "contract_address_chain", "result"
)
SELECT
    "hash", "block_number", "transaction_index", "from", "from", "to", "to", "nonce", "contract_address", "contract_address", "result"
FROM indexed_transactions_old;

CREATE TABLE indexed_events (
    "transaction_hash"  VARCHAR NOT NULL,
    "block_number"      BIGINT  NOT NULL,
    "transaction_index" INT     NOT NULL,
    "log_index"         INT     NOT NULL,
    "signature"         VARCHAR NOT NULL,
    PRIMARY KEY ("block_number", "transaction_index", "log_index"),
    FOREIGN KEY ("block_number", "transaction_index") REFERENCES indexed_transactions ("block_number", "transaction_index") ON DELETE CASCADE
);
CREATE INDEX indexed_events_signature ON indexed_events("signature");
CREATE INDEX indexed_events_transaction_hash ON indexed_events("transaction_hash");

INSERT INTO indexed_events ("transaction_hash", "block_number", "transaction_index", "log_index", "signature")
SELECT "transaction_hash", "block_number", "transaction_index", "log_index", "signature"
FROM indexed_events_old;

DROP TABLE indexed_events_old;
DROP TABLE indexed_transactions_old;

PRAGMA foreign_keys=on;
