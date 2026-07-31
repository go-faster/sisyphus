-- Create "telegram_peers" table
CREATE TABLE "telegram_peers" ("id" uuid NOT NULL, "peer_type" character varying NOT NULL, "peer_id" bigint NOT NULL, "access_hash" bigint NULL, "username" character varying NULL, "title" character varying NULL, "last_seen_at" timestamptz NOT NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "telegrampeer_peer_type_peer_id" to table: "telegram_peers"
CREATE UNIQUE INDEX "telegrampeer_peer_type_peer_id" ON "telegram_peers" ("peer_type", "peer_id");
