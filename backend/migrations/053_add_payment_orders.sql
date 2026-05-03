CREATE TABLE IF NOT EXISTS payment_orders (
    id TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    amount DECIMAL(20,8) NOT NULL,
    amount_cents BIGINT NOT NULL,
    currency VARCHAR(3) NOT NULL DEFAULT 'cny',
    provider VARCHAR(32) NOT NULL DEFAULT 'stripe',
    payment_method VARCHAR(32) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'created',
    stripe_session_id TEXT UNIQUE,
    stripe_payment_intent_id TEXT,
    stripe_event_id TEXT,
    checkout_url TEXT,
    failure_reason TEXT,
    raw_event JSONB,
    expires_at TIMESTAMPTZ,
    paid_at TIMESTAMPTZ,
    credited_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT payment_orders_amount_positive CHECK (amount > 0),
    CONSTRAINT payment_orders_amount_cents_positive CHECK (amount_cents > 0)
);

CREATE INDEX IF NOT EXISTS idx_payment_orders_user_id ON payment_orders(user_id);
CREATE INDEX IF NOT EXISTS idx_payment_orders_status ON payment_orders(status);
CREATE INDEX IF NOT EXISTS idx_payment_orders_created_at ON payment_orders(created_at);
CREATE INDEX IF NOT EXISTS idx_payment_orders_stripe_session_id ON payment_orders(stripe_session_id);
