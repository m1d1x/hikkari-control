-- Pauses over 40 ms in a voice series (client 1.0.92): Discord's jitter buffer
-- holds about 20 ms, so a call hears a pause past it before one past 60 ms
-- (hikkari-client plans/2026-11-18-live-traffic-observability.md, O3). Null
-- for series of clients that do not count it.
ALTER TABLE voice_runs ADD COLUMN IF NOT EXISTS gaps40 integer;
