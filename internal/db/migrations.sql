ALTER TABLE blocked_channels ADD COLUMN IF NOT EXISTS name TEXT;
--
ALTER TABLE blocked_channels ADD COLUMN IF NOT EXISTS thumbnail_url TEXT;
--
ALTER TABLE push_devices ADD COLUMN IF NOT EXISTS p256dh TEXT;
--
ALTER TABLE push_devices ADD COLUMN IF NOT EXISTS auth_secret TEXT;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS subtitles_enabled BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS notification_popups_enabled BOOLEAN NOT NULL DEFAULT true;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS default_playback_speed DOUBLE PRECISION NOT NULL DEFAULT 1.0;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS hold_playback_speed DOUBLE PRECISION NOT NULL DEFAULT 2.0;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS default_subtitle_language TEXT NOT NULL DEFAULT '';
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS default_audio_language TEXT NOT NULL DEFAULT '';
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS prefer_original_language BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS enable_high_quality_playback BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS sponsor_block_mode TEXT NOT NULL DEFAULT 'auto_skip';
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS sponsor_block_category_actions TEXT NOT NULL DEFAULT '{}';
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS sponsor_block_minimum_duration INTEGER NOT NULL DEFAULT 0;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS sponsor_block_show_current_segment BOOLEAN NOT NULL DEFAULT true;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS sponsor_block_show_chapters BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS sponsor_block_show_full_video_labels BOOLEAN NOT NULL DEFAULT true;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS sponsor_block_manual_skip_on_full_video BOOLEAN NOT NULL DEFAULT true;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS sponsor_block_skip_non_music_only_on_music_videos BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS sponsor_block_mute_instead_of_skip BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS hide_home_recommendations BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS hide_continue_watching BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS hide_related_videos BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS hide_end_screens BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS hide_comments BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS hide_shorts BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS hide_subscription_live_streams BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS hide_members_only_content BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS disable_watch_history BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS skip_playlist_autoplay_screen BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS hide_upcoming_playlist_videos BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS ambient_light BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS ambient_light_spread INTEGER NOT NULL DEFAULT 100;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS ambient_light_blur INTEGER NOT NULL DEFAULT 50;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS ambient_light_brightness INTEGER NOT NULL DEFAULT 100;
--
DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'settings' AND column_name = 'autoplay_on_open') THEN ALTER TABLE settings ADD COLUMN autoplay_on_open BOOLEAN NOT NULL DEFAULT true; UPDATE settings SET autoplay_on_open = autoplay; END IF; END $$;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS autoplay_countdown_seconds INTEGER NOT NULL DEFAULT 10;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS subscription_sync_interval INTEGER NOT NULL DEFAULT 0;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS default_landing_page TEXT NOT NULL DEFAULT 'home';
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS video_grid_columns INTEGER NOT NULL DEFAULT 0;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS related_video_size TEXT NOT NULL DEFAULT 'default';
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS access_mode TEXT NOT NULL DEFAULT 'unrestricted';
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS access_mode_admin_managed BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS access_mode_admin_managed_at BIGINT NOT NULL DEFAULT 0;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS dearrow_enabled BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS dearrow_title_mode TEXT NOT NULL DEFAULT 'dearrow';
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS dearrow_thumbnail_mode TEXT NOT NULL DEFAULT 'dearrow_or_random';
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS dearrow_trust_mode TEXT NOT NULL DEFAULT 'accepted';
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS seekbar_icon TEXT NOT NULL DEFAULT 'typetype';
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS seekbar_icon_url TEXT NOT NULL DEFAULT '';
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS player_engine TEXT NOT NULL DEFAULT 'videojs';
--
ALTER TABLE settings ALTER COLUMN player_engine SET DEFAULT 'videojs';
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS hide_watched_subscription_videos BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS watched_threshold_percent INTEGER NOT NULL DEFAULT 0;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS mini_player_enabled BOOLEAN NOT NULL DEFAULT true;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS hide_search_shorts BOOLEAN NOT NULL DEFAULT true;
--
ALTER TABLE history ADD COLUMN IF NOT EXISTS channel_avatar TEXT NOT NULL DEFAULT '';
--
ALTER TABLE history ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
--
ALTER TABLE favorites ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
--
ALTER TABLE watch_later ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
--
ALTER TABLE progress ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
--
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
--
ALTER TABLE search_history ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
--
ALTER TABLE playlists ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
--
ALTER TABLE playlist_videos ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
--
ALTER TABLE playlist_videos ADD COLUMN IF NOT EXISTS channel_name TEXT NOT NULL DEFAULT '';
--
ALTER TABLE playlist_videos ADD COLUMN IF NOT EXISTS channel_url TEXT NOT NULL DEFAULT '';
--
ALTER TABLE playlist_videos ADD COLUMN IF NOT EXISTS channel_avatar TEXT NOT NULL DEFAULT '';
--
ALTER TABLE playlist_videos ADD COLUMN IF NOT EXISTS view_count BIGINT NOT NULL DEFAULT 0;
--
ALTER TABLE playlist_videos ADD COLUMN IF NOT EXISTS added_at BIGINT NOT NULL DEFAULT 0;
--
ALTER TABLE playlist_videos ADD COLUMN IF NOT EXISTS published_at BIGINT NOT NULL DEFAULT -1;
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
--
ALTER TABLE settings ADD COLUMN IF NOT EXISTS caption_styles TEXT NOT NULL DEFAULT '{}';
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS access_mode TEXT NOT NULL DEFAULT 'unrestricted';
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS rss_enabled BOOLEAN NOT NULL DEFAULT FALSE;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS rss_public_base_url TEXT;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS rss_max_feeds_per_user INTEGER NOT NULL DEFAULT 10;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS rss_max_items INTEGER NOT NULL DEFAULT 50;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS rss_minimum_poll_minutes INTEGER NOT NULL DEFAULT 5;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS rss_rate_limit_per_minute INTEGER NOT NULL DEFAULT 30;
--
ALTER TABLE blocked_channels ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
--
ALTER TABLE blocked_channels ADD COLUMN IF NOT EXISTS scope TEXT NOT NULL DEFAULT 'user';
--
ALTER TABLE blocked_videos ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
--
ALTER TABLE blocked_videos ADD COLUMN IF NOT EXISTS scope TEXT NOT NULL DEFAULT 'user';
--
ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_url TEXT;
--
ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_type TEXT;
--
ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_code TEXT;
--
ALTER TABLE users ADD COLUMN IF NOT EXISTS public_username TEXT;
--
ALTER TABLE users ADD COLUMN IF NOT EXISTS bio TEXT;
--
ALTER TABLE youtube_takeout_import_jobs ADD COLUMN IF NOT EXISTS preview_json TEXT;
--
ALTER TABLE youtube_sessions ADD COLUMN IF NOT EXISTS auth_user INTEGER NOT NULL DEFAULT 0;
--
ALTER TABLE bug_reports ALTER COLUMN github_issue_url TYPE TEXT;
--
ALTER TABLE focus_settings ADD COLUMN IF NOT EXISTS study_group_id TEXT;
--
ALTER TABLE focus_settings ADD COLUMN IF NOT EXISTS schedule TEXT NOT NULL DEFAULT 'always';
--
ALTER TABLE focus_settings ADD COLUMN IF NOT EXISTS timezone TEXT;
--
ALTER TABLE focus_settings ADD COLUMN IF NOT EXISTS strict_blocking BOOLEAN NOT NULL DEFAULT FALSE;
--
ALTER TABLE focus_settings ADD COLUMN IF NOT EXISTS grayscale_thumbnails BOOLEAN NOT NULL DEFAULT FALSE;
--
ALTER TABLE focus_settings ALTER COLUMN strict_blocking SET DEFAULT FALSE;
--
ALTER TABLE focus_settings ALTER COLUMN grayscale_thumbnails SET DEFAULT FALSE;
--
ALTER TABLE youtube_sessions ADD COLUMN IF NOT EXISTS auth_type TEXT NOT NULL DEFAULT 'cookies';
--
ALTER TABLE youtube_sessions ADD COLUMN IF NOT EXISTS encrypted_access_token TEXT NOT NULL DEFAULT '';
--
ALTER TABLE youtube_sessions ADD COLUMN IF NOT EXISTS encrypted_refresh_token TEXT NOT NULL DEFAULT '';
--
ALTER TABLE youtube_sessions ADD COLUMN IF NOT EXISTS access_expires_at BIGINT NOT NULL DEFAULT 0;
--
ALTER TABLE youtube_sessions ADD COLUMN IF NOT EXISTS account_label TEXT NOT NULL DEFAULT '';
--
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS refresh_token_hash TEXT;
--
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS created_at BIGINT NOT NULL DEFAULT 0;
--
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS revoked_at BIGINT;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS name TEXT DEFAULT 'TypeType';
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS tagline TEXT;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS logo_url TEXT;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS banner_url TEXT;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS min_android_client_version TEXT;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS active_sessions_enabled BOOLEAN NOT NULL DEFAULT false;
--
UPDATE sessions SET created_at = CASE WHEN created_at = 0 THEN expires_at - 86400000 ELSE created_at END;
--
CREATE UNIQUE INDEX IF NOT EXISTS sessions_refresh_token_hash_unique ON sessions (refresh_token_hash);
--
ALTER TABLE users ADD COLUMN IF NOT EXISTS oidc_issuer TEXT;
--
ALTER TABLE users ADD COLUMN IF NOT EXISTS oidc_subject TEXT;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS local_login_enabled BOOLEAN NOT NULL DEFAULT true;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS oidc_auto_redirect BOOLEAN NOT NULL DEFAULT false;
--
CREATE UNIQUE INDEX IF NOT EXISTS users_oidc_identity_unique ON users (oidc_issuer, oidc_subject);
--
INSERT INTO profile_accounts (profile_id, owner_user_id, display_name, is_default, last_used_at, created_at, updated_at)
SELECT id, id, COALESCE(NULLIF(name, ''), 'Profile'), TRUE, 0, created_at, updated_at
FROM users
ON CONFLICT (profile_id) DO NOTHING;
--
CREATE INDEX IF NOT EXISTS profile_accounts_owner_idx ON profile_accounts (owner_user_id);
--
WITH ranked AS (
SELECT profile_id,
row_number() OVER (
PARTITION BY owner_user_id
ORDER BY is_default DESC, last_used_at DESC, created_at ASC, profile_id ASC
) AS profile_rank
FROM profile_accounts
)
UPDATE profile_accounts AS profiles
SET is_default = (ranked.profile_rank = 1)
FROM ranked
WHERE profiles.profile_id = ranked.profile_id;
--
CREATE UNIQUE INDEX IF NOT EXISTS profile_accounts_one_default_idx ON profile_accounts (owner_user_id) WHERE is_default;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS youtube_remote_login_enabled BOOLEAN NOT NULL DEFAULT false;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS device_login_enabled BOOLEAN NOT NULL DEFAULT TRUE;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS bilibili_mirror_probing_enabled BOOLEAN NOT NULL DEFAULT FALSE;
--
CREATE UNIQUE INDEX IF NOT EXISTS users_public_username_unique ON users (public_username);
--
DO $$
DECLARE current_name text;
DECLARE current_cols text;
BEGIN
SELECT c.conname, string_agg(a.attname, ', ' ORDER BY array_position(i.indkey, a.attnum))
INTO current_name, current_cols
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_index i ON i.indrelid = t.oid AND i.indisprimary
JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(i.indkey)
WHERE t.relname = 'subscriptions'
AND t.relnamespace = current_schema()::regnamespace
AND c.contype = 'p'
GROUP BY c.conname;
IF current_cols IS DISTINCT FROM 'user_id, channel_url' THEN
IF current_name IS NOT NULL THEN
EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I', 'subscriptions', current_name);
END IF;
EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I PRIMARY KEY (user_id, channel_url)', 'subscriptions', 'subscriptions_pkey');
END IF;
END $$;;
--
DO $$
DECLARE current_name text;
DECLARE current_cols text;
BEGIN
SELECT c.conname, string_agg(a.attname, ', ' ORDER BY array_position(i.indkey, a.attnum))
INTO current_name, current_cols
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_index i ON i.indrelid = t.oid AND i.indisprimary
JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(i.indkey)
WHERE t.relname = 'progress'
AND t.relnamespace = current_schema()::regnamespace
AND c.contype = 'p'
GROUP BY c.conname;
IF current_cols IS DISTINCT FROM 'user_id, video_url' THEN
IF current_name IS NOT NULL THEN
EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I', 'progress', current_name);
END IF;
EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I PRIMARY KEY (user_id, video_url)', 'progress', 'progress_pkey');
END IF;
END $$;;
--
DO $$
DECLARE current_name text;
DECLARE current_cols text;
BEGIN
SELECT c.conname, string_agg(a.attname, ', ' ORDER BY array_position(i.indkey, a.attnum))
INTO current_name, current_cols
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_index i ON i.indrelid = t.oid AND i.indisprimary
JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(i.indkey)
WHERE t.relname = 'favorites'
AND t.relnamespace = current_schema()::regnamespace
AND c.contype = 'p'
GROUP BY c.conname;
IF current_cols IS DISTINCT FROM 'user_id, video_url' THEN
IF current_name IS NOT NULL THEN
EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I', 'favorites', current_name);
END IF;
EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I PRIMARY KEY (user_id, video_url)', 'favorites', 'favorites_pkey');
END IF;
END $$;;
--
DO $$
DECLARE current_name text;
DECLARE current_cols text;
BEGIN
SELECT c.conname, string_agg(a.attname, ', ' ORDER BY array_position(i.indkey, a.attnum))
INTO current_name, current_cols
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_index i ON i.indrelid = t.oid AND i.indisprimary
JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(i.indkey)
WHERE t.relname = 'watch_later'
AND t.relnamespace = current_schema()::regnamespace
AND c.contype = 'p'
GROUP BY c.conname;
IF current_cols IS DISTINCT FROM 'user_id, url' THEN
IF current_name IS NOT NULL THEN
EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I', 'watch_later', current_name);
END IF;
EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I PRIMARY KEY (user_id, url)', 'watch_later', 'watch_later_pkey');
END IF;
END $$;;
--
DO $$
DECLARE current_name text;
DECLARE current_cols text;
BEGIN
SELECT c.conname, string_agg(a.attname, ', ' ORDER BY array_position(i.indkey, a.attnum))
INTO current_name, current_cols
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_index i ON i.indrelid = t.oid AND i.indisprimary
JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(i.indkey)
WHERE t.relname = 'blocked_channels'
AND t.relnamespace = current_schema()::regnamespace
AND c.contype = 'p'
GROUP BY c.conname;
IF current_cols IS DISTINCT FROM 'user_id, channel_url' THEN
IF current_name IS NOT NULL THEN
EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I', 'blocked_channels', current_name);
END IF;
EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I PRIMARY KEY (user_id, channel_url)', 'blocked_channels', 'blocked_channels_pkey');
END IF;
END $$;;
--
DO $$
DECLARE current_name text;
DECLARE current_cols text;
BEGIN
SELECT c.conname, string_agg(a.attname, ', ' ORDER BY array_position(i.indkey, a.attnum))
INTO current_name, current_cols
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_index i ON i.indrelid = t.oid AND i.indisprimary
JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(i.indkey)
WHERE t.relname = 'blocked_keywords'
AND t.relnamespace = current_schema()::regnamespace
AND c.contype = 'p'
GROUP BY c.conname;
IF current_cols IS DISTINCT FROM 'user_id, keyword' THEN
IF current_name IS NOT NULL THEN
EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I', 'blocked_keywords', current_name);
END IF;
EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I PRIMARY KEY (user_id, keyword)', 'blocked_keywords', 'blocked_keywords_pkey');
END IF;
END $$;;
--
DO $$
DECLARE current_name text;
DECLARE current_cols text;
BEGIN
SELECT c.conname, string_agg(a.attname, ', ' ORDER BY array_position(i.indkey, a.attnum))
INTO current_name, current_cols
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_index i ON i.indrelid = t.oid AND i.indisprimary
JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(i.indkey)
WHERE t.relname = 'blocked_videos'
AND t.relnamespace = current_schema()::regnamespace
AND c.contype = 'p'
GROUP BY c.conname;
IF current_cols IS DISTINCT FROM 'user_id, video_url' THEN
IF current_name IS NOT NULL THEN
EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I', 'blocked_videos', current_name);
END IF;
EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I PRIMARY KEY (user_id, video_url)', 'blocked_videos', 'blocked_videos_pkey');
END IF;
END $$;;
--
DO $$
DECLARE current_name text;
DECLARE current_cols text;
BEGIN
SELECT c.conname, string_agg(a.attname, ', ' ORDER BY array_position(i.indkey, a.attnum))
INTO current_name, current_cols
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_index i ON i.indrelid = t.oid AND i.indisprimary
JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(i.indkey)
WHERE t.relname = 'settings'
AND t.relnamespace = current_schema()::regnamespace
AND c.contype = 'p'
GROUP BY c.conname;
IF current_cols IS DISTINCT FROM 'user_id' THEN
IF current_name IS NOT NULL THEN
EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I', 'settings', current_name);
END IF;
EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I PRIMARY KEY (user_id)', 'settings', 'settings_pkey');
END IF;
END $$;;
--
CREATE EXTENSION IF NOT EXISTS pg_trgm;
--
CREATE INDEX IF NOT EXISTS idx_history_user_watched_id ON history (user_id, watched_at DESC, id DESC);
--
CREATE INDEX IF NOT EXISTS idx_playlist_videos_user_playlist_position ON playlist_videos (user_id, playlist_id, position);
--
CREATE INDEX IF NOT EXISTS idx_subscriptions_user_subscribed_at ON subscriptions (user_id, subscribed_at DESC);
--
CREATE INDEX IF NOT EXISTS idx_subscriptions_name_trgm ON subscriptions USING gin (lower(name) gin_trgm_ops);
--
CREATE INDEX IF NOT EXISTS idx_subscriptions_channel_url_trgm ON subscriptions USING gin (lower(channel_url) gin_trgm_ops);
--
CREATE INDEX IF NOT EXISTS idx_favorites_user_favorited_at ON favorites (user_id, favorited_at DESC);
--
CREATE INDEX IF NOT EXISTS idx_watch_later_user_added_at ON watch_later (user_id, added_at DESC);
--
CREATE INDEX IF NOT EXISTS idx_search_history_user_searched_at ON search_history (user_id, searched_at DESC);
--
CREATE INDEX IF NOT EXISTS idx_playlists_user_created_at ON playlists (user_id, created_at DESC);
--
CREATE INDEX IF NOT EXISTS idx_saved_playlists_user_saved_at ON saved_playlists (user_id, saved_at DESC);
--
CREATE UNIQUE INDEX IF NOT EXISTS idx_saved_playlists_user_url ON saved_playlists (user_id, url);
--
CREATE INDEX IF NOT EXISTS idx_history_title_trgm ON history USING gin (lower(title) gin_trgm_ops);
--
CREATE INDEX IF NOT EXISTS idx_history_channel_name_trgm ON history USING gin (lower(channel_name) gin_trgm_ops);
--
CREATE INDEX IF NOT EXISTS idx_playlist_videos_user_metadata_repair ON playlist_videos (user_id) WHERE title LIKE 'YouTube video %' OR thumbnail LIKE 'https://i.ytimg.com/vi/%' OR duration <= 0 OR channel_name = '' OR channel_url = '';
--
CREATE INDEX IF NOT EXISTS idx_watch_later_user_metadata_repair ON watch_later (user_id, added_at DESC) WHERE title LIKE 'YouTube video %' OR thumbnail LIKE 'https://i.ytimg.com/vi/%' OR duration <= 0 OR channel_name = '' OR channel_url = '';
--
CREATE INDEX IF NOT EXISTS idx_favorites_user_metadata_repair ON favorites (user_id, favorited_at DESC) WHERE title LIKE 'YouTube video %' OR thumbnail LIKE 'https://i.ytimg.com/vi/%' OR duration <= 0 OR channel_name = '' OR channel_url = '';
--
-- go:canonicalize_subscriptions;
--
ALTER TABLE favorites ADD COLUMN IF NOT EXISTS title TEXT NOT NULL DEFAULT '';
--
ALTER TABLE favorites ADD COLUMN IF NOT EXISTS thumbnail TEXT NOT NULL DEFAULT '';
--
ALTER TABLE favorites ADD COLUMN IF NOT EXISTS duration BIGINT NOT NULL DEFAULT 0;
--
ALTER TABLE favorites ADD COLUMN IF NOT EXISTS channel_name TEXT NOT NULL DEFAULT '';
--
ALTER TABLE favorites ADD COLUMN IF NOT EXISTS channel_url TEXT NOT NULL DEFAULT '';
--
ALTER TABLE favorites ADD COLUMN IF NOT EXISTS channel_avatar TEXT NOT NULL DEFAULT '';
--
UPDATE history
SET thumbnail = 'https://i.ytimg.com/vi/' || substring(url from '(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})') || '/hqdefault.jpg'
WHERE thumbnail = '' AND substring(url from '(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})') IS NOT NULL;
--
UPDATE history
SET title = 'YouTube video ' || substring(url from '(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})')
WHERE title = '' AND substring(url from '(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})') IS NOT NULL;
--
UPDATE playlist_videos
SET thumbnail = 'https://i.ytimg.com/vi/' || substring(url from '(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})') || '/hqdefault.jpg'
WHERE thumbnail = '' AND substring(url from '(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})') IS NOT NULL;
--
UPDATE playlist_videos
SET title = 'YouTube video ' || substring(url from '(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})')
WHERE title = '' AND substring(url from '(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})') IS NOT NULL;
--
UPDATE watch_later
SET thumbnail = 'https://i.ytimg.com/vi/' || substring(url from '(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})') || '/hqdefault.jpg'
WHERE thumbnail = '' AND substring(url from '(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})') IS NOT NULL;
--
UPDATE watch_later
SET title = 'YouTube video ' || substring(url from '(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})')
WHERE title = '' AND substring(url from '(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})') IS NOT NULL;
--
UPDATE favorites
SET thumbnail = 'https://i.ytimg.com/vi/' || substring(video_url from '(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})') || '/hqdefault.jpg'
WHERE thumbnail = '' AND substring(video_url from '(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})') IS NOT NULL;
--
UPDATE favorites
SET title = 'YouTube video ' || substring(video_url from '(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})')
WHERE title = '' AND substring(video_url from '(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})') IS NOT NULL;
--
UPDATE favorites fav
SET title = CASE WHEN fav.title = '' THEN media.title ELSE fav.title END,
thumbnail = CASE WHEN fav.thumbnail = '' THEN media.thumbnail ELSE fav.thumbnail END,
duration = CASE WHEN fav.duration = 0 THEN media.duration ELSE fav.duration END,
channel_name = CASE WHEN fav.channel_name = '' THEN media.channel_name ELSE fav.channel_name END,
channel_url = CASE WHEN fav.channel_url = '' THEN media.channel_url ELSE fav.channel_url END,
channel_avatar = CASE WHEN fav.channel_avatar = '' THEN media.channel_avatar ELSE fav.channel_avatar END
FROM history media
WHERE fav.user_id = media.user_id AND fav.video_url = media.url;
--
UPDATE favorites fav
SET title = CASE WHEN fav.title = '' THEN media.title ELSE fav.title END,
thumbnail = CASE WHEN fav.thumbnail = '' THEN media.thumbnail ELSE fav.thumbnail END,
duration = CASE WHEN fav.duration = 0 THEN media.duration ELSE fav.duration END,
channel_name = CASE WHEN fav.channel_name = '' THEN media.channel_name ELSE fav.channel_name END,
channel_url = CASE WHEN fav.channel_url = '' THEN media.channel_url ELSE fav.channel_url END,
channel_avatar = CASE WHEN fav.channel_avatar = '' THEN media.channel_avatar ELSE fav.channel_avatar END
FROM playlist_videos media
WHERE fav.user_id = media.user_id AND fav.video_url = media.url;
--
UPDATE favorites fav
SET title = CASE WHEN fav.title = '' THEN media.title ELSE fav.title END,
thumbnail = CASE WHEN fav.thumbnail = '' THEN media.thumbnail ELSE fav.thumbnail END,
duration = CASE WHEN fav.duration = 0 THEN media.duration ELSE fav.duration END
FROM watch_later media
WHERE fav.user_id = media.user_id AND fav.video_url = media.url;
--
UPDATE subscriptions
SET avatar_url = media.channel_avatar
FROM history media
WHERE subscriptions.avatar_url = ''
AND media.channel_avatar <> ''
AND media.user_id = subscriptions.user_id
AND media.channel_url = subscriptions.channel_url;
--
UPDATE subscriptions
SET avatar_url = media.channel_avatar
FROM playlist_videos media
WHERE subscriptions.avatar_url = ''
AND media.channel_avatar <> ''
AND media.user_id = subscriptions.user_id
AND media.channel_url = subscriptions.channel_url;
--
UPDATE history media
SET channel_avatar = subscriptions.avatar_url
FROM subscriptions
WHERE media.channel_avatar = ''
AND subscriptions.avatar_url <> ''
AND media.user_id = subscriptions.user_id
AND media.channel_url = subscriptions.channel_url;
--
UPDATE playlist_videos media
SET channel_avatar = subscriptions.avatar_url
FROM subscriptions
WHERE media.channel_avatar = ''
AND subscriptions.avatar_url <> ''
AND media.user_id = subscriptions.user_id
AND media.channel_url = subscriptions.channel_url;
--
UPDATE favorites fav
SET channel_avatar = subscriptions.avatar_url
FROM subscriptions
WHERE fav.channel_avatar = ''
AND subscriptions.avatar_url <> ''
AND fav.user_id = subscriptions.user_id
AND fav.channel_url = subscriptions.channel_url;
--
ALTER TABLE watch_later ADD COLUMN IF NOT EXISTS channel_name TEXT NOT NULL DEFAULT '';
--
ALTER TABLE watch_later ADD COLUMN IF NOT EXISTS channel_url TEXT NOT NULL DEFAULT '';
--
ALTER TABLE watch_later ADD COLUMN IF NOT EXISTS channel_avatar TEXT NOT NULL DEFAULT '';
--
ALTER TABLE watch_later ADD COLUMN IF NOT EXISTS view_count BIGINT NOT NULL DEFAULT 0;
--
ALTER TABLE watch_later ADD COLUMN IF NOT EXISTS published_at BIGINT NOT NULL DEFAULT -1;
--
ALTER TABLE favorites ADD COLUMN IF NOT EXISTS view_count BIGINT NOT NULL DEFAULT 0;
--
ALTER TABLE favorites ADD COLUMN IF NOT EXISTS published_at BIGINT NOT NULL DEFAULT -1;
--
ALTER TABLE watch_later ADD COLUMN IF NOT EXISTS metadata_refreshed_at BIGINT NOT NULL DEFAULT 0;
--
ALTER TABLE favorites ADD COLUMN IF NOT EXISTS metadata_refreshed_at BIGINT NOT NULL DEFAULT 0;
--
ALTER TABLE playlist_videos ADD COLUMN IF NOT EXISTS metadata_refreshed_at BIGINT NOT NULL DEFAULT 0;
--
UPDATE watch_later target
SET channel_name = CASE WHEN target.channel_name = '' THEN source.channel_name ELSE target.channel_name END,
channel_url = CASE WHEN target.channel_url = '' THEN source.channel_url ELSE target.channel_url END,
channel_avatar = CASE WHEN target.channel_avatar = '' THEN source.channel_avatar ELSE target.channel_avatar END,
view_count = CASE WHEN target.view_count = 0 THEN source.view_count ELSE target.view_count END,
published_at = CASE WHEN target.published_at <= 0 THEN source.published_at ELSE target.published_at END
FROM playlist_videos source
WHERE target.user_id = source.user_id AND target.url = source.url;
--
UPDATE favorites target
SET view_count = CASE WHEN target.view_count = 0 THEN source.view_count ELSE target.view_count END,
published_at = CASE WHEN target.published_at <= 0 THEN source.published_at ELSE target.published_at END
FROM playlist_videos source
WHERE target.user_id = source.user_id AND target.video_url = source.url;
--
UPDATE favorites target
SET view_count = CASE WHEN target.view_count = 0 THEN source.view_count ELSE target.view_count END,
published_at = CASE WHEN target.published_at <= 0 THEN source.published_at ELSE target.published_at END
FROM watch_later source
WHERE target.user_id = source.user_id AND target.video_url = source.url;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS recommender_enabled BOOLEAN NOT NULL DEFAULT FALSE;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS recommender_default_on BOOLEAN NOT NULL DEFAULT FALSE;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS recommender_default_algorithm TEXT;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS recommender_allowed_algorithms TEXT NOT NULL DEFAULT '';
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS recommender_timeout_ms INTEGER NOT NULL DEFAULT 0;
--
ALTER TABLE admin_settings ADD COLUMN IF NOT EXISTS recommender_stream_requests_per_minute INTEGER NOT NULL DEFAULT 0;
--
ALTER TABLE recommender_feedback ADD COLUMN IF NOT EXISTS reason TEXT;
--
ALTER TABLE recommender_feedback ADD COLUMN IF NOT EXISTS title TEXT;
--
ALTER TABLE recommender_feedback ADD COLUMN IF NOT EXISTS channel_name TEXT;
--
ALTER TABLE recommender_feedback ADD COLUMN IF NOT EXISTS thumbnail_url TEXT;
--
ALTER TABLE bilibili_sessions ADD COLUMN IF NOT EXISTS encrypted_refresh_token TEXT;
--
ALTER TABLE bilibili_sessions ADD COLUMN IF NOT EXISTS encrypted_pending_confirm_token TEXT;
--
ALTER TABLE bilibili_sessions ADD COLUMN IF NOT EXISTS refresh_checked_at BIGINT NOT NULL DEFAULT 0;
--
ALTER TABLE bilibili_sessions ADD COLUMN IF NOT EXISTS refresh_claimed_until BIGINT NOT NULL DEFAULT 0;
