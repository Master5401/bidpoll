CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE IF NOT EXISTS polls (
                                     id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
                                     title TEXT NOT NULL,
                                     created_by TEXT NOT NULL,
                                     created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
                                     is_locked BOOLEAN DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS poll_options (
                                            id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
                                            poll_id UUID NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
                                            text TEXT NOT NULL,
                                            state VARCHAR(10) NOT NULL DEFAULT 'FREE',
                                            held_by TEXT,
                                            locked_at TIMESTAMP WITH TIME ZONE,
                                            UNIQUE(poll_id, text)
);

CREATE INDEX IF NOT EXISTS idx_poll_options_state ON poll_options(state);
CREATE INDEX IF NOT EXISTS idx_poll_options_held_by ON poll_options(held_by);