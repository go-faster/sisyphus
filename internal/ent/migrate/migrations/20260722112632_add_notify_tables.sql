-- Create "notify_users" table
CREATE TABLE "notify_users" ("id" uuid NOT NULL, "telegram_user_id" bigint NOT NULL, "telegram_access_hash" bigint NULL, "gitlab_username" character varying NULL, "jira_account_id" character varying NULL, "jira_display_name" character varying NULL, "enabled" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "notifyuser_gitlab_username" to table: "notify_users"
CREATE UNIQUE INDEX "notifyuser_gitlab_username" ON "notify_users" ("gitlab_username");
-- Create index "notifyuser_jira_account_id" to table: "notify_users"
CREATE UNIQUE INDEX "notifyuser_jira_account_id" ON "notify_users" ("jira_account_id");
-- Create index "notifyuser_telegram_user_id" to table: "notify_users"
CREATE UNIQUE INDEX "notifyuser_telegram_user_id" ON "notify_users" ("telegram_user_id");
-- Create "notifications" table
CREATE TABLE "notifications" ("id" uuid NOT NULL, "dedup_key" character varying NOT NULL, "channel" character varying NOT NULL, "telegram_user_id" bigint NULL, "telegram_access_hash" bigint NULL, "source" character varying NOT NULL, "event_type" character varying NOT NULL, "text" text NOT NULL, "url" character varying NULL, "status" character varying NOT NULL DEFAULT 'pending', "attempts" bigint NOT NULL DEFAULT 0, "error" text NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "delivered_at" timestamptz NULL, "user_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "notifications_notify_users_notifications" FOREIGN KEY ("user_id") REFERENCES "notify_users" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "notification_dedup_key" to table: "notifications"
CREATE UNIQUE INDEX "notification_dedup_key" ON "notifications" ("dedup_key");
-- Create index "notification_status" to table: "notifications"
CREATE INDEX "notification_status" ON "notifications" ("status");
-- Create "notify_subscriptions" table
CREATE TABLE "notify_subscriptions" ("id" uuid NOT NULL, "source" character varying NOT NULL, "event_types" jsonb NOT NULL, "filters" jsonb NOT NULL DEFAULT '{}', "enabled" boolean NOT NULL DEFAULT true, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, "user_id" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "notify_subscriptions_notify_users_subscriptions" FOREIGN KEY ("user_id") REFERENCES "notify_users" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "notifysubscription_filters" to table: "notify_subscriptions"
CREATE INDEX "notifysubscription_filters" ON "notify_subscriptions" USING gin ("filters");
-- Create index "notifysubscription_user_id_source" to table: "notify_subscriptions"
CREATE UNIQUE INDEX "notifysubscription_user_id_source" ON "notify_subscriptions" ("user_id", "source");
