ALTER TABLE indexed_transactions ADD COLUMN "from_chain" TEXT;
ALTER TABLE indexed_transactions ADD COLUMN "to_chain" TEXT;
ALTER TABLE indexed_transactions ADD COLUMN "contract_address_chain" TEXT;

UPDATE indexed_transactions SET "from_chain" = "from" WHERE "from_chain" IS NULL;
UPDATE indexed_transactions SET "to_chain" = "to" WHERE "to" IS NOT NULL AND "to_chain" IS NULL;
UPDATE indexed_transactions SET "contract_address_chain" = "contract_address" WHERE "contract_address" IS NOT NULL AND "contract_address_chain" IS NULL;

ALTER TABLE indexed_transactions ALTER COLUMN "from" DROP NOT NULL;
DROP INDEX IF EXISTS indexed_transaction_from_nonce;
CREATE UNIQUE INDEX indexed_transaction_from_chain_nonce ON indexed_transactions("from_chain","nonce");
