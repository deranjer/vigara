ALTER TABLE container_image_updates ADD COLUMN tracking_tag TEXT NOT NULL DEFAULT '' CHECK(length(tracking_tag)<=128);
