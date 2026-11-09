-- A short provider progress window for arr queue completion estimates. The router keeps at most six samples.
-- +goose Up
CREATE TABLE download_progress (
    download_id INTEGER NOT NULL REFERENCES download (id) ON DELETE CASCADE,
    sampled_at  INTEGER NOT NULL,
    provider    TEXT NOT NULL,
    torrent_id  TEXT NOT NULL,
    progress    REAL NOT NULL,
    PRIMARY KEY (download_id, sampled_at)
);

-- +goose Down
DROP TABLE download_progress;
