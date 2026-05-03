-- Reverses 0001_init.up.sql. Drops in reverse-dependency order.
DROP TABLE IF EXISTS connector;
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS audit;
DROP TABLE IF EXISTS incident;
DROP TYPE  IF EXISTS connector_status;
DROP TYPE  IF EXISTS audit_result;
DROP TYPE  IF EXISTS incident_status;
DROP TYPE  IF EXISTS incident_severity;
