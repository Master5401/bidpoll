ALTER TABLE polls ADD COLUMN IF NOT EXISTS channel_id TEXT;
ALTER TABLE polls ADD COLUMN IF NOT EXISTS message_id TEXT;

-- Needed to ensure the buttons render in the exact same order every time
ALTER TABLE poll_options ADD COLUMN IF NOT EXISTS created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW();