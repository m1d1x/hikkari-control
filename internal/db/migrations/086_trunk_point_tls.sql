CREATE TABLE entry_trunk_point_tls (
    core_box text PRIMARY KEY REFERENCES entry_boxes(name),
    relay_port integer NOT NULL CHECK (relay_port BETWEEN 1 AND 65535),
    allow_legacy boolean NOT NULL
);

CREATE TABLE entry_trunk_point_tls_names (
    core_box text NOT NULL REFERENCES entry_trunk_point_tls(core_box) ON DELETE CASCADE,
    point_box text NOT NULL REFERENCES entry_boxes(name),
    domain text NOT NULL REFERENCES entry_receiver_certs(domain),
    applied_issued_at timestamptz,
    PRIMARY KEY (core_box, point_box),
    UNIQUE (core_box, domain),
    CHECK (core_box <> point_box)
);

CREATE TABLE entry_trunk_point_tls_uses (
    entry_box text NOT NULL REFERENCES trunk_entries(entry_box) ON DELETE CASCADE,
    core_box text NOT NULL REFERENCES entry_trunk_point_tls(core_box) ON DELETE CASCADE,
    stand boolean NOT NULL,
    points text[] NOT NULL,
    PRIMARY KEY (entry_box, stand)
);
