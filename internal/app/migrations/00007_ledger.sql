-- +goose Up
CREATE TABLE findings (
    id             uuid PRIMARY KEY,
    review_id      uuid NOT NULL REFERENCES reviews (id),
    fingerprint    text NOT NULL,
    seq            integer NOT NULL,
    severity       text NOT NULL CHECK (severity IN ('blocker', 'major', 'minor', 'nit')),
    category       text NOT NULL,
    title          text NOT NULL,
    body           text NOT NULL DEFAULT '',
    path           text NOT NULL,
    start_line     integer NOT NULL,
    end_line       integer NOT NULL,
    commit_sha     text NOT NULL,
    evidence       jsonb NOT NULL,
    suggestion     text NOT NULL DEFAULT '',
    status         text NOT NULL CHECK (status IN ('open', 'fixed', 'withdrawn', 'dismissed', 'conceded', 'resolved_by_human')),
    blocking       boolean NOT NULL DEFAULT false,
    challenge      jsonb,
    -- previous is the fixed finding that this one repeats (§9.3).
    previous_id    uuid REFERENCES findings (id),
    thread_node_id text NOT NULL DEFAULT '',
    comment_id     bigint,
    first_round_id uuid NOT NULL REFERENCES rounds (id),
    last_round_id  uuid NOT NULL REFERENCES rounds (id),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (review_id, seq)
);
CREATE INDEX findings_review_fingerprint ON findings (review_id, fingerprint);

CREATE TABLE finding_events (
    id         uuid PRIMARY KEY,
    finding_id uuid NOT NULL REFERENCES findings (id),
    at         timestamptz NOT NULL DEFAULT now(),
    round_id   uuid REFERENCES rounds (id),
    actor      text NOT NULL,
    type       text NOT NULL CHECK (type IN ('raised', 'reraised', 'still_open', 'fixed', 'withdrawn', 'dismissed', 'conceded', 'held',
                                             'resolved_by_human', 'reopened', 'suppressed')),
    reason     text NOT NULL DEFAULT ''
);
CREATE INDEX finding_events_finding ON finding_events (finding_id, at);

-- publications records each GitHub write of a round, so a retry of Publish
-- finds what an earlier attempt wrote (§10.5).
CREATE TABLE publications (
    round_id   uuid NOT NULL REFERENCES rounds (id),
    key        text NOT NULL,
    github_id  bigint,
    node_id    text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (round_id, key)
);

CREATE TABLE admissions (
    id            uuid PRIMARY KEY,
    round_id      uuid REFERENCES rounds (id),
    run_id        uuid REFERENCES agent_runs (id),
    repository_id uuid NOT NULL REFERENCES repositories (id),
    kind          text NOT NULL CHECK (kind IN ('round', 'reply', 'fix')),
    priority      integer NOT NULL,
    enqueued_at   timestamptz NOT NULL DEFAULT now(),
    admitted_at   timestamptz,
    released_at   timestamptz,
    -- heartbeat_at shows that the waiting or running activity is alive.
    heartbeat_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX admissions_waiting ON admissions (priority, enqueued_at) WHERE admitted_at IS NULL AND released_at IS NULL;

ALTER TABLE rounds ADD COLUMN failure_reason text NOT NULL DEFAULT '';
ALTER TABLE rounds ADD COLUMN cancelled_by text NOT NULL DEFAULT '';
ALTER TABLE rounds ADD COLUMN agent_run_id uuid REFERENCES agent_runs (id);
ALTER TABLE rounds ADD COLUMN workflow_run_id text NOT NULL DEFAULT '';
-- ledger_outcome is what ReconcileLedger changed, so a retry of Publish posts the same.
ALTER TABLE rounds ADD COLUMN ledger_outcome jsonb;
-- preview is what Publish posts, rendered; shadow mode shows only this (§10.1).
ALTER TABLE rounds ADD COLUMN preview jsonb;

-- +goose Down
ALTER TABLE rounds DROP COLUMN preview;
ALTER TABLE rounds DROP COLUMN ledger_outcome;
ALTER TABLE rounds DROP COLUMN workflow_run_id;
ALTER TABLE rounds DROP COLUMN agent_run_id;
ALTER TABLE rounds DROP COLUMN cancelled_by;
ALTER TABLE rounds DROP COLUMN failure_reason;
DROP TABLE admissions;
DROP TABLE publications;
DROP TABLE finding_events;
DROP TABLE findings;
