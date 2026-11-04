-- Run in the WHMCS database, in the same connection as matomo-import.sql.
DROP TEMPORARY TABLE IF EXISTS moddhosting_matomo_input;
CREATE TEMPORARY TABLE moddhosting_matomo_input (
    input_row INT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    service_id LONGTEXT,
    matomo_id LONGTEXT
) ENGINE=InnoDB;

-- Replace these examples with your complete mapping (WHMCS service IDs, not client IDs):
-- INSERT INTO moddhosting_matomo_input (service_id, matomo_id) VALUES
-- ('123', '45'), ('124', '46');

-- Alternatively, use a headerless UTF-8 CSV with exactly two columns:
-- LOAD DATA LOCAL INFILE '/absolute/path/matomo.csv'
-- INTO TABLE moddhosting_matomo_input
-- FIELDS TERMINATED BY ',' OPTIONALLY ENCLOSED BY '"'
-- LINES TERMINATED BY '\n' (service_id, matomo_id);
