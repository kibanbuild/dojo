-- +goose Up
-- Catalog tables (design 3.3, 3.8): content loaded at deploy from catalog.yaml and ticket.yaml by
-- the migration role (the catalog load job). The API never parses YAML per request; it only reads.
-- The CHECKs mirror the caps in 3.8, so a bad load fails in the database as well as in the loader.
-- Position uniques are DEFERRABLE so one load transaction can reorder rows without a transient clash.
CREATE TABLE paths (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id     uuid NOT NULL REFERENCES orgs (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    slug       text NOT NULL UNIQUE CHECK (char_length(slug) <= 64 AND slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    title      text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 80),
    status     text NOT NULL CHECK (status IN ('available', 'planned')),
    position   integer NOT NULL CHECK (position BETWEEN 1 AND 999),
    CONSTRAINT paths_position_key UNIQUE (position) DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE roles (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id     uuid NOT NULL REFERENCES orgs (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    slug       text NOT NULL UNIQUE CHECK (char_length(slug) <= 64 AND slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    title      text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 80),
    kind       text NOT NULL CHECK (kind IN ('foundations', 'role')),
    has_exam   boolean NOT NULL,
    advanced   boolean NOT NULL DEFAULT false
);

-- A role has no home path and no order of its own: membership and order live here, so Foundations
-- is one roles row shared by every path.
CREATE TABLE path_roles (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id     uuid NOT NULL REFERENCES orgs (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    path_id    uuid NOT NULL REFERENCES paths (id) ON DELETE CASCADE,
    role_id    uuid NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    position   integer NOT NULL CHECK (position BETWEEN 1 AND 999),
    CONSTRAINT path_roles_path_role_key UNIQUE (path_id, role_id),
    CONSTRAINT path_roles_path_position_key UNIQUE (path_id, position) DEFERRABLE INITIALLY DEFERRED
);

CREATE INDEX path_roles_role_id_idx ON path_roles (role_id);

-- Tags: 0 to 8 distinct values of [a-z0-9-]{1,24} (ticket.yaml). A function because a CHECK cannot
-- hold a subquery; it is pure, so it is IMMUTABLE.
CREATE FUNCTION catalog_tags_valid(tags text[]) RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE
SET search_path = pg_catalog
AS $$
    SELECT coalesce(array_ndims(tags), 1) = 1
       AND cardinality(tags) BETWEEN 0 AND 8
       AND (SELECT count(DISTINCT t) = cardinality(tags) AND coalesce(bool_and(t ~ '^[a-z0-9-]{1,24}$'), true)
              FROM unnest(tags) AS t)
$$;

-- Hints: one to three strings of 1 to 1,000 characters, none null.
CREATE FUNCTION catalog_hints_valid(hints text[]) RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE
SET search_path = pg_catalog
AS $$
    SELECT array_ndims(hints) = 1
       AND cardinality(hints) BETWEEN 1 AND 3
       AND (SELECT bool_and(h IS NOT NULL AND char_length(h) BETWEEN 1 AND 1000) FROM unnest(hints) AS h)
$$;

-- id is text, not uuid: the ticket id <role-slug>/<ticket-slug> is the key the CLI and events use.
-- role_id is null for practice items. Tickets fill brief, hints and the bundle columns; other kinds
-- leave them null until their milestone, but a ticket may not.
CREATE TABLE catalog_items (
    id             text PRIMARY KEY CHECK (char_length(id) BETWEEN 3 AND 64
                                           AND id ~ '^[a-z0-9]+(-[a-z0-9]+)*(/[a-z0-9]+(-[a-z0-9]+)*)*$'),
    org_id         uuid NOT NULL REFERENCES orgs (id),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    role_id        uuid REFERENCES roles (id),
    kind           text NOT NULL CHECK (kind IN ('ticket', 'writing', 'quiz', 'story', 'feedback', 'practice')),
    position       integer NOT NULL CHECK (position BETWEEN 1 AND 999),
    title          text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 80),
    est_minutes    integer NOT NULL CHECK (est_minutes BETWEEN 1 AND 240),
    tags           text[] NOT NULL DEFAULT '{}' CHECK (catalog_tags_valid(tags)),
    brief          text CHECK (octet_length(brief) BETWEEN 1 AND 8192),
    hints          text[] CHECK (catalog_hints_valid(hints)),
    awards_mark    text CHECK (char_length(awards_mark) BETWEEN 1 AND 64),
    catalog_version text NOT NULL CHECK (catalog_version ~ '^[0-9a-f]{12}$'),
    bundle_key     text CHECK (char_length(bundle_key) BETWEEN 1 AND 512),
    bundle_sha256  text CHECK (bundle_sha256 ~ '^[0-9a-f]{64}$'),
    bundle_bytes   integer CHECK (bundle_bytes BETWEEN 1 AND 1048576),
    CONSTRAINT catalog_items_bundle_all_or_none CHECK (
        (bundle_key IS NULL) = (bundle_sha256 IS NULL) AND (bundle_key IS NULL) = (bundle_bytes IS NULL)),
    CONSTRAINT catalog_items_ticket_complete CHECK (
        kind <> 'ticket' OR (brief IS NOT NULL AND hints IS NOT NULL AND bundle_key IS NOT NULL
                             AND role_id IS NOT NULL)),
    -- A ticket id is exactly <role-slug>/<ticket-slug> (3.8); other kinds keep the looser shape.
    CONSTRAINT catalog_items_ticket_id_shape CHECK (
        kind <> 'ticket' OR id ~ '^[a-z0-9]+(-[a-z0-9]+)*/[a-z0-9]+(-[a-z0-9]+)*$'),
    CONSTRAINT catalog_items_role_position_key UNIQUE (role_id, position) DEFERRABLE INITIALLY DEFERRED
);

CREATE INDEX catalog_items_role_id_idx ON catalog_items (role_id);

-- One row, upserted by every successful load in the same transaction. Before the first load there
-- is no row. It is where GET /v1/catalog reads the top-level version.
CREATE TABLE catalog_state (
    id              smallint PRIMARY KEY CHECK (id = 1),
    org_id          uuid NOT NULL REFERENCES orgs (id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    catalog_version text NOT NULL CHECK (catalog_version ~ '^[0-9a-f]{12}$')
);

-- Grants (design 3.3, 3.7): the API reads the catalog and nothing else. Every write comes from the
-- load job running as the migration role. dojo_admin has no grant: no operator screen reads these
-- tables yet; A2's progress view gets SELECT with the ticket_progress table that it joins.
GRANT SELECT ON paths, roles, path_roles, catalog_items, catalog_state TO dojo_api;

-- +goose Down
DROP TABLE catalog_state;
DROP TABLE catalog_items;
DROP FUNCTION catalog_hints_valid(text[]);
DROP FUNCTION catalog_tags_valid(text[]);
DROP TABLE path_roles;
DROP TABLE roles;
DROP TABLE paths;
