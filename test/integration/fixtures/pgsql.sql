-- The PostgreSQL counterpart. Deliberately smaller than the MySQL fixture:
-- rehost skips the stored-URL rewrite for pg dumps (they are COPY-format
-- data, not INSERT statements) and warns about it, so the serialized-string
-- machinery is not on trial here. What is on trial is pg_dump over SSH with
-- the password staged in a pgpass file, and ON_ERROR_STOP on the way back in.

CREATE TABLE wide_chars (
  id   integer PRIMARY KEY,
  note text NOT NULL
);

INSERT INTO wide_chars (id, note) VALUES
  (1, 'rocket 🚀 four-byte'),
  (2, 'accents éàüñ and CJK 日本語テキスト'),
  (3, 'quote '' backslash \ tab	here');

CREATE TABLE options (
  id           integer PRIMARY KEY,
  option_name  text NOT NULL,
  option_value text
);

INSERT INTO options (id, option_name, option_value) VALUES
  (1, 'url_plain', 'https://old.example.com/landing'),
  (2, 'null_value', NULL),
  (3, 'empty_value', '');

CREATE TABLE blobs (
  id   integer PRIMARY KEY,
  data bytea NOT NULL
);

INSERT INTO blobs (id, data) VALUES
  (1, '\x00010203fffefd'::bytea),
  (2, '\xdeadbeef'::bytea);

CREATE TABLE bulk_rows (
  id      integer PRIMARY KEY,
  payload text NOT NULL
);

INSERT INTO bulk_rows (id, payload)
SELECT n, 'row ' || n || ' https://old.example.com/page/' || n
FROM generate_series(1, 2000) AS n;

CREATE VIEW recent_options AS
  SELECT id, option_name FROM options WHERE option_value IS NOT NULL;
