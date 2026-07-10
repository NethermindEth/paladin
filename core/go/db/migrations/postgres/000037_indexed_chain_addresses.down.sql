DROP INDEX IF EXISTS indexed_transaction_from_chain_nonce;
UPDATE indexed_transactions SET "from" = COALESCE("from", SUBSTRING(MD5(COALESCE("from_chain", '')) FROM 1 FOR 40));
ALTER TABLE indexed_transactions ALTER COLUMN "from" SET NOT NULL;
ALTER TABLE indexed_transactions DROP COLUMN "contract_address_chain";
ALTER TABLE indexed_transactions DROP COLUMN "to_chain";
ALTER TABLE indexed_transactions DROP COLUMN "from_chain";
CREATE UNIQUE INDEX indexed_transaction_from_nonce ON indexed_transactions("from","nonce");
