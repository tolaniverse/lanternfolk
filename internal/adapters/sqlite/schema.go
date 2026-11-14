package sqlite

// migrations are applied in order; PRAGMA user_version records how many ran.
// Never edit a released migration: append a new one.
var migrations = []string{
	// 1: slice 1, the workspace loop. Every workspace table leads with
	// workspace_id, so sharding by workspace stays a mechanical change.
	`
CREATE TABLE users (
	id         TEXT PRIMARY KEY,
	handle     TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL
);
CREATE TABLE sessions (
	token_hash BLOB PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id),
	expires_at INTEGER NOT NULL
);
CREATE TABLE workspaces (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	slug       TEXT NOT NULL UNIQUE,
	seq        INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE TABLE gates (
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	ord          INTEGER NOT NULL,
	kind         TEXT NOT NULL,
	prompt       TEXT NOT NULL,
	salt         BLOB NOT NULL,
	hash         BLOB NOT NULL,
	PRIMARY KEY (workspace_id, ord)
);
CREATE TABLE members (
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	user_id      TEXT NOT NULL REFERENCES users(id),
	role         TEXT NOT NULL,
	joined_at    INTEGER NOT NULL,
	PRIMARY KEY (workspace_id, user_id)
);
CREATE TABLE channels (
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	id           TEXT NOT NULL,
	name         TEXT NOT NULL,
	PRIMARY KEY (workspace_id, id)
);
CREATE TABLE messages (
	workspace_id  TEXT NOT NULL,
	id            TEXT NOT NULL,
	channel_id    TEXT NOT NULL,
	author_id     TEXT NOT NULL REFERENCES users(id),
	body          TEXT NOT NULL,
	client_cmd_id TEXT NOT NULL DEFAULT '',
	seq           INTEGER NOT NULL,
	created_at    INTEGER NOT NULL,
	PRIMARY KEY (workspace_id, id),
	FOREIGN KEY (workspace_id, channel_id) REFERENCES channels(workspace_id, id)
);
CREATE UNIQUE INDEX messages_idempotency ON messages (workspace_id, author_id, client_cmd_id) WHERE client_cmd_id <> '';
CREATE INDEX messages_by_channel ON messages (workspace_id, channel_id, id);
CREATE TABLE outbox (
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	seq          INTEGER NOT NULL,
	type         TEXT NOT NULL,
	payload      TEXT NOT NULL,
	created_at   INTEGER NOT NULL,
	published    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (workspace_id, seq)
);
CREATE INDEX outbox_unpublished ON outbox (workspace_id, seq) WHERE published = 0;
`,
}
