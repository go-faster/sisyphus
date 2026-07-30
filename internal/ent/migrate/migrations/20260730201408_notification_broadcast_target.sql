-- Modify "notifications" table
ALTER TABLE "notifications" DROP CONSTRAINT "notifications_users_notifications", ALTER COLUMN "user_id" DROP NOT NULL, ADD COLUMN "peer_type" character varying NOT NULL DEFAULT 'user', ADD CONSTRAINT "notifications_users_notifications" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
