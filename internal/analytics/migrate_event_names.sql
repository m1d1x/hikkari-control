-- ClickHouse migration: normalize legacy event_type names to namespaced format
-- Run once after deploying the code changes.
-- Each ALTER TABLE UPDATE is async in ClickHouse — use MUTATIONS SYNC if needed.

-- Legacy flat names → namespaced format
ALTER TABLE events UPDATE event_type = 'app:open'                WHERE event_type = 'app_open';
ALTER TABLE events UPDATE event_type = 'ui:page_view'            WHERE event_type = 'page_view';
ALTER TABLE events UPDATE event_type = 'chat:message_sent'       WHERE event_type = 'message_sent';
ALTER TABLE events UPDATE event_type = 'chat:message_read'       WHERE event_type = 'message_read';
ALTER TABLE events UPDATE event_type = 'discover:open'           WHERE event_type = 'discover_open';
ALTER TABLE events UPDATE event_type = 'discover:scroll'         WHERE event_type = 'discover_scroll';
ALTER TABLE events UPDATE event_type = 'discover:search'         WHERE event_type = 'discover_search';
ALTER TABLE events UPDATE event_type = 'discover:card_impression' WHERE event_type = 'card_impression';
ALTER TABLE events UPDATE event_type = 'discover:card_click'     WHERE event_type = 'card_preview';
ALTER TABLE events UPDATE event_type = 'discover:card_join'      WHERE event_type = 'card_join';
ALTER TABLE events UPDATE event_type = 'server:joined'           WHERE event_type = 'server_join';
ALTER TABLE events UPDATE event_type = 'tag:filter_applied'      WHERE event_type = 'tag_filter_applied';
ALTER TABLE events UPDATE event_type = 'discover:feedback'       WHERE event_type = 'discover_feedback';

-- Verify: check for any remaining non-namespaced event types
-- SELECT event_type, COUNT(*) FROM events
-- WHERE event_type NOT LIKE '%:%'
-- GROUP BY event_type ORDER BY COUNT(*) DESC;
