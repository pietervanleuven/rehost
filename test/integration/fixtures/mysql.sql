-- An adversarial corpus, not a realistic site. A default CMS install is a
-- friendlier input than this: everything here exists because it has broken a
-- dump, a transfer or a search-replace somewhere in the wild.

SET NAMES utf8mb4;

-- Four-byte characters, combining marks and a NUL-adjacent control byte: the
-- charset pinning in the dump connection has to carry these through byte-exact.
CREATE TABLE wide_chars (
  id   INT PRIMARY KEY,
  note VARCHAR(255) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO wide_chars (id, note) VALUES
  (1, 'rocket 🚀 four-byte'),
  (2, 'family 👩‍👩‍👧‍👦 zero-width joiners'),
  (3, 'accents éàüñ and CJK 日本語テキスト'),
  (4, 'quote '' backslash \\ tab\there'),
  (5, 'trailing spaces   ');

-- Serialized PHP holding the site URL. The byte-length prefix is the whole
-- point: a naive string replace with a different-length host silently
-- corrupts the structure, and unserialize() then returns false — the classic
-- "migrated site loads blank" failure.
CREATE TABLE options (
  id           INT PRIMARY KEY,
  option_name  VARCHAR(191) NOT NULL,
  option_value LONGTEXT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO options (id, option_name, option_value) VALUES
  (1, 'urls_serialized',
      'a:2:{s:4:"home";s:23:"https://old.example.com";s:7:"siteurl";s:23:"https://old.example.com";}'),
  (2, 'url_plain', 'https://old.example.com/landing'),
  (3, 'url_protocol_relative', '//old.example.com/assets/app.js'),
  -- Stored as JSON with escaped slashes, the way WordPress writes settings.
  (4, 'url_json_escaped', '{"endpoint":"https:\\/\\/old.example.com\\/api"}'),
  (5, 'nested_serialized',
      'a:1:{s:6:"config";a:1:{s:3:"url";s:23:"https://old.example.com";}}'),
  (6, 'null_value', NULL),
  (7, 'empty_value', '');

-- Binary data must survive the round trip untouched.
CREATE TABLE blobs (
  id   INT PRIMARY KEY,
  data VARBINARY(64) NOT NULL
) ENGINE=InnoDB;

INSERT INTO blobs (id, data) VALUES
  (1, UNHEX('00010203FFFEFD')),
  (2, UNHEX('DEADBEEF'));

-- Enough rows that the dump actually streams rather than fitting in one read.
CREATE TABLE bulk_rows (
  id      INT PRIMARY KEY,
  payload VARCHAR(255) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE audit_log (
  id       INT AUTO_INCREMENT PRIMARY KEY,
  message  VARCHAR(255) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

DELIMITER //

CREATE PROCEDURE fill_bulk_rows()
BEGIN
  DECLARE i INT DEFAULT 1;
  WHILE i <= 2000 DO
    INSERT INTO bulk_rows (id, payload)
      VALUES (i, CONCAT('row ', i, ' https://old.example.com/page/', i));
    SET i = i + 1;
  END WHILE;
END //

-- A routine and a trigger: mysqldump only emits these with --routines
-- --triggers, so their presence in the dump proves those flags survived.
CREATE FUNCTION site_url_of(kind VARCHAR(32)) RETURNS VARCHAR(255) DETERMINISTIC
BEGIN
  RETURN CONCAT('https://old.example.com/', kind);
END //

CREATE TRIGGER wide_chars_audit AFTER INSERT ON wide_chars
FOR EACH ROW
BEGIN
  INSERT INTO audit_log (message) VALUES (CONCAT('inserted ', NEW.id));
END //

DELIMITER ;

CALL fill_bulk_rows();

-- A view depends on a base table, so the restore order has to be right.
CREATE VIEW recent_options AS
  SELECT id, option_name FROM options WHERE option_value IS NOT NULL;
