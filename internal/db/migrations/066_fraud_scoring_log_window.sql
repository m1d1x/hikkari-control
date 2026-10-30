-- 066 — the scoring log gets an index on time (plan 42 К7).
--
-- Every row of `fraud_scoring_log` is one verdict as it was passed, and until
-- К7 nothing read the table by time: the only index is on `user_id`, for
-- "what happened to this account". The fraud screen asks the opposite
-- question — "what happened over the last week, split by outcome, and how are
-- the scores piled" — which is two sequential scans of the whole log on every
-- reload of the screen.
--
-- The index carries `total_score` and `action` so both of the screen's
-- queries are answered from it: the outcome series groups by action, the
-- histogram buckets the score, and neither needs the row.

CREATE INDEX idx_fraud_scoring_log_window
    ON fraud_scoring_log (scored_at) INCLUDE (action, total_score);
