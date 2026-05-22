-- Ops Monitoring: add min_requests column to ops_alert_rules
-- Allows rules to require a minimum request count before rate-based metrics
-- (success_rate, error_rate, upstream_error_rate) are evaluated.
-- Default 0 means no minimum (preserves existing behavior).

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

ALTER TABLE ops_alert_rules
    ADD COLUMN IF NOT EXISTS min_requests INT NOT NULL DEFAULT 0;
