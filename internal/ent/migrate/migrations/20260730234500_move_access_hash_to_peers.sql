-- Move Telegram access hashes out of the notification tables and into
-- telegram_peers, which is now the only place a peer's address lives.
--
-- The hashes already stored are the only ones the bot has: it captures a hash
-- from the update that carried the peer, so dropping these columns without
-- copying them first would leave every known user and chat unaddressable
-- until they happened to message the bot again.
INSERT INTO "telegram_peers" ("id", "peer_type", "peer_id", "access_hash", "last_seen_at", "created_at")
SELECT gen_random_uuid(), 'user', "telegram_user_id", "telegram_access_hash", now(), now()
FROM "users"
WHERE "telegram_access_hash" IS NOT NULL
ON CONFLICT ("peer_type", "peer_id") DO NOTHING;

INSERT INTO "telegram_peers" ("id", "peer_type", "peer_id", "access_hash", "title", "last_seen_at", "created_at")
SELECT gen_random_uuid(), "peer_type", "peer_id", "access_hash", "title", now(), now()
FROM "notify_chats"
WHERE "access_hash" IS NOT NULL
ON CONFLICT ("peer_type", "peer_id") DO NOTHING;

ALTER TABLE "users" DROP COLUMN "telegram_access_hash";
ALTER TABLE "notify_chats" DROP COLUMN "access_hash";
ALTER TABLE "notifications" DROP COLUMN "telegram_access_hash";
