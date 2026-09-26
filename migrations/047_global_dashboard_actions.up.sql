CREATE TABLE global_dashboard_actions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ref_type        VARCHAR(20) NOT NULL CHECK (ref_type IN ('fault', 'ticket', 'maintenance', 'task')),
    ref_id          UUID NOT NULL,
    action          VARCHAR(20) NOT NULL CHECK (action IN ('accept', 'done', 'discard', 'wait')),
    comment         TEXT NOT NULL CHECK (length(btrim(comment)) >= 8),
    action_user_id  UUID NOT NULL REFERENCES users(id),
    assigned_to     UUID REFERENCES users(id),
    follow_up_date  DATE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((action = 'accept' AND assigned_to IS NOT NULL) OR action <> 'accept'),
    CHECK ((action = 'wait' AND follow_up_date IS NOT NULL) OR action <> 'wait')
);

CREATE INDEX idx_global_dashboard_actions_record
    ON global_dashboard_actions (ref_type, ref_id, created_at DESC);
