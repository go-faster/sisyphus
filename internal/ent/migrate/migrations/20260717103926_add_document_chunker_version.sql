-- Modify "documents" table
ALTER TABLE "documents" ADD COLUMN "chunker_version" bigint NOT NULL DEFAULT 0;
