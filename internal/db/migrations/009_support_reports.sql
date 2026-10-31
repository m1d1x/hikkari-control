-- Support reports (tickets) submitted by clients with sanitized diagnostic logs.
CREATE TABLE IF NOT EXISTS support_reports (
    id bigserial PRIMARY KEY,
    install_id text NOT NULL,
    platform text NOT NULL,
    version text NOT NULL,
    message text NOT NULL,
    logs text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'answered', 'closed')),
    messages jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS support_reports_install_id ON support_reports (install_id, created_at DESC);
CREATE INDEX IF NOT EXISTS support_reports_status ON support_reports (status, created_at DESC);
