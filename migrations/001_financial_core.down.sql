BEGIN;
DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS inbox_messages;
DROP TABLE IF EXISTS wallet_ledger_entries;
DROP FUNCTION IF EXISTS reject_ledger_mutation();
DROP TABLE IF EXISTS wager_transactions;
DROP TABLE IF EXISTS wallets;
COMMIT;
