-- +goose Up
CREATE TABLE orgs (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    slug       text NOT NULL UNIQUE,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO orgs (slug, name) VALUES ('public', 'Public');

-- +goose Down
DROP TABLE orgs;
