-- Devices (webhooks): what the phone apps, autobrr, decypharr and Usenet bridges need from
-- the arr views beyond 00012, and the outbound webhooks (internal/notify). Conventions as in 00001. See
-- docs/design/devices.md.

-- +goose Up

-- Notifications installed through a view (POST notification: Ruddarr's webhook, a Usenet bridge's). resource is the
-- NotificationResource as last sent (JSON, id left out); the columns are what the sender needs. Ids are global.
CREATE TABLE arrview_notification (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    view            TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    implementation  TEXT    NOT NULL,
    resource        TEXT    NOT NULL,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE INDEX arrview_notification_view ON arrview_notification (view);

-- Events the notifier has emitted, so each is sent once: kind (grab, download, health) and a reference (the
-- download id; 0 for health, whose last message set is in notify_state).
CREATE TABLE notify_event (
    kind        TEXT    NOT NULL,
    ref         INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (kind, ref)
);

-- Small notifier state: since (when it first ran: older downloads are never announced), health (the last health
-- messages sent, JSON).
CREATE TABLE notify_state (
    key         TEXT    PRIMARY KEY,
    value       TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- The outbox. target is notification:<id> (installed through a view) or policy:<key>; view the arr view whose
-- payload it is ('' for native events). state pending (due at next_at), sent, failed (retries used up) or dropped
-- (the target is gone, disabled or not allowed). The sender never blocks the pipeline: it only reads this table.
CREATE TABLE notify_delivery (
    id           INTEGER PRIMARY KEY,
    target       TEXT    NOT NULL,
    event        TEXT    NOT NULL,
    view         TEXT    NOT NULL DEFAULT '',
    payload      TEXT    NOT NULL,
    state        TEXT    NOT NULL CHECK (state IN ('pending', 'sent', 'failed', 'dropped')),
    attempts     INTEGER NOT NULL DEFAULT 0,
    next_at      INTEGER NOT NULL,
    last_status  INTEGER,
    last_error   TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    sent_at      INTEGER
);
CREATE INDEX notify_delivery_due ON notify_delivery (state, next_at);

-- +goose Down
DROP TABLE notify_delivery;
DROP TABLE notify_state;
DROP TABLE notify_event;
DROP TABLE arrview_notification;
