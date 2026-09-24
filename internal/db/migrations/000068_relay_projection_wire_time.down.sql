-- Rebuild disposable ordering metadata using the restored decoder's clock.
-- No canonical events or projected entities are deleted.
DELETE FROM relay_projection_meta;
