-- The owner's own name for a hoster on the fleet screen (19.11: «легко
-- переименовывать»), the way entry_boxes.label (076) names a machine. Only
-- shown: the provider's code stays what every query and the hoster's API key
-- go by. A row is removed, not emptied, when the name is taken back.
CREATE TABLE IF NOT EXISTS entry_provider_labels (
	provider   text PRIMARY KEY,
	label      text NOT NULL CHECK (label <> ''),
	updated_at timestamptz NOT NULL DEFAULT now()
);
