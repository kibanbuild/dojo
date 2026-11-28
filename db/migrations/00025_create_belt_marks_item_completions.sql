-- +goose Up
-- Between-tickets catalog (design 3.10): kind payloads, practice difficulty, belt marks and item completions.
-- The CHECKs keep every row complete for its kind, so a load that forgets a column fails in the database.
CREATE TABLE belt_marks (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id     uuid NOT NULL REFERENCES orgs (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    slug       text NOT NULL UNIQUE CHECK (char_length(slug) <= 64 AND slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    belt       text NOT NULL CHECK (belt IN ('white', 'yellow', 'orange', 'green', 'blue', 'brown', 'black')),
    title      text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 80),
    summary    text NOT NULL CHECK (char_length(summary) BETWEEN 1 AND 200),
    position   integer NOT NULL CHECK (position BETWEEN 1 AND 999),
    CONSTRAINT belt_marks_belt_position_key UNIQUE (belt, position) DEFERRABLE INITIALLY DEFERRED
);

ALTER TABLE catalog_items
    ADD COLUMN payload    jsonb CHECK (jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 65536),
    ADD COLUMN difficulty text CHECK (difficulty IN ('easy', 'medium', 'hard')),
    ADD CONSTRAINT catalog_items_awards_mark_fkey FOREIGN KEY (awards_mark) REFERENCES belt_marks (slug),
    ADD CONSTRAINT catalog_items_payload_kind CHECK ((payload IS NOT NULL) = (kind IN ('writing', 'quiz', 'story', 'feedback'))),
    ADD CONSTRAINT catalog_items_awards_mark_kind CHECK (awards_mark IS NULL OR kind IN ('writing', 'feedback')),
    ADD CONSTRAINT catalog_items_difficulty_kind CHECK (difficulty IS NULL OR kind = 'practice'),
    ADD CONSTRAINT catalog_items_role_kind CHECK ((role_id IS NULL) = (kind = 'practice')),
    ADD CONSTRAINT catalog_items_practice_complete CHECK (
        kind <> 'practice' OR (brief IS NOT NULL AND hints IS NOT NULL AND bundle_key IS NOT NULL));

-- item_id has no cascade: a catalog load cannot drop an item a learner has completed.
CREATE TABLE item_completions (
    org_id       uuid NOT NULL REFERENCES orgs (id),
    learner_id   uuid NOT NULL REFERENCES learners (id) ON DELETE CASCADE,
    item_id      text NOT NULL REFERENCES catalog_items (id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (learner_id, item_id)
);

-- Grants (design 3.7, 3.10). The load job writes belt_marks as the migration role. The API inserts a
-- completion once and never changes or removes it; dojo_admin reads both for the progress view (A2).
-- payload and difficulty are covered by the table SELECT of 00012.
GRANT SELECT ON belt_marks, item_completions TO dojo_api, dojo_admin;
GRANT INSERT (org_id, learner_id, item_id) ON item_completions TO dojo_api;

-- +goose Down
DROP TABLE item_completions;
ALTER TABLE catalog_items
    DROP CONSTRAINT catalog_items_practice_complete,
    DROP CONSTRAINT catalog_items_role_kind,
    DROP CONSTRAINT catalog_items_difficulty_kind,
    DROP CONSTRAINT catalog_items_awards_mark_kind,
    DROP CONSTRAINT catalog_items_payload_kind,
    DROP CONSTRAINT catalog_items_awards_mark_fkey,
    DROP COLUMN difficulty,
    DROP COLUMN payload;
DROP TABLE belt_marks;
