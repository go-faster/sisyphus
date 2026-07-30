-- Create "notify_chats" table
CREATE TABLE "notify_chats" ("id" uuid NOT NULL, "peer_type" character varying NOT NULL DEFAULT 'channel', "peer_id" bigint NOT NULL, "access_hash" bigint NULL, "title" character varying NULL, "enabled" boolean NOT NULL DEFAULT true, "added_by" bigint NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "notifychat_enabled" to table: "notify_chats"
CREATE INDEX "notifychat_enabled" ON "notify_chats" ("enabled");
-- Create index "notifychat_peer_type_peer_id" to table: "notify_chats"
CREATE UNIQUE INDEX "notifychat_peer_type_peer_id" ON "notify_chats" ("peer_type", "peer_id");
-- Modify "notifications" table
ALTER TABLE "notifications" DROP CONSTRAINT "notifications_users_notifications", ALTER COLUMN "user_id" DROP NOT NULL, ADD COLUMN "peer_type" character varying NOT NULL DEFAULT 'user', ADD CONSTRAINT "notifications_users_notifications" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
