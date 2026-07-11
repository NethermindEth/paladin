CREATE TABLE stellar_event_payloads (
    "block_number"      BIGINT  NOT NULL,
    "transaction_index" BIGINT  NOT NULL,
    "log_index"         BIGINT  NOT NULL,
    "emitter"           TEXT,
    "topics"            TEXT,
    "data"              TEXT,
    PRIMARY KEY ("block_number", "transaction_index", "log_index"),
    FOREIGN KEY ("block_number", "transaction_index", "log_index") REFERENCES indexed_events ("block_number", "transaction_index", "log_index") ON DELETE CASCADE
);
