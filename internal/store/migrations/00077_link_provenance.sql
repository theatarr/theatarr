-- Link provenance. Conventions as in 00001.
--
-- link.adopted_from: "<arr instance> <arr path>" of a link adopt made (or a follower refresh moved), NULL for every
-- other link and once an import of Theatarr's own takes the row over. The follower refresh and the router's offer sources
-- read it; before, they read the latest "adopted from ..." library_action, which retention deletes after its window.
-- Backfilled from that action.
--
-- Indexes for the retention download rule: each old failed download is checked against these tables, and most of
-- them are kept (blocklisted), so without them every batch scanned the tables once per candidate.

-- +goose Up
ALTER TABLE link ADD COLUMN adopted_from TEXT;

UPDATE link SET adopted_from = (
    SELECT CASE WHEN instr(a.detail, ' (refresh: ') > 0
        THEN substr(a.detail, 14, instr(a.detail, ' (refresh: ') - 14) ELSE substr(a.detail, 14) END
    FROM library_action a
    WHERE a.id = (SELECT MAX(id) FROM library_action WHERE link_id = link.id AND kind = 'link' AND mode = 'applied')
      AND a.detail LIKE 'adopted from %')
WHERE download_id IS NULL;

CREATE INDEX link_download ON link (download_id);
CREATE INDEX router_blocklist_download ON router_blocklist (download_id);
CREATE INDEX router_offer_source ON router_offer (source_kind, source_id);
CREATE INDEX upgrade_hold_download ON upgrade_hold (download_id);

-- +goose Down
DROP INDEX upgrade_hold_download;
DROP INDEX router_offer_source;
DROP INDEX router_blocklist_download;
DROP INDEX link_download;
ALTER TABLE link DROP COLUMN adopted_from;
