-- Backfill delivery jobs for notifications enqueued before the outbox moved
-- onto queue_jobs. Without this, every notification already sitting in
-- status='pending' at upgrade time has no queue job, so no drainer would ever
-- claim it and it would never be delivered.
--
-- The delivery's ID is the notification's ID, and its dedup key is that same
-- ID as text, matching what internal/notify/store.Enqueue writes.
INSERT INTO queue_jobs (
	id, queue, dedup_key, payload,
	status, attempts, max_attempts, visible_at, created_at, updated_at
)
SELECT
	n.id,
	'notify.' || n.channel,
	n.id::text,
	convert_to(
		json_build_object(
			'telegram_user_id', COALESCE(n.telegram_user_id, 0),
			'telegram_access_hash', COALESCE(n.telegram_access_hash, 0),
			'text', n.text,
			'url', COALESCE(n.url, '')
		)::text,
		'UTF8'
	),
	'pending',
	COALESCE(n.attempts, 0),
	5,
	now(),
	n.created_at,
	now()
FROM notifications n
WHERE n.status = 'pending'
ON CONFLICT (queue, dedup_key) DO NOTHING;
