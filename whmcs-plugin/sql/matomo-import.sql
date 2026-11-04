-- Requires CREATE ROUTINE. Defines a helper; run CALL with 0 to preview, 1 to apply.
-- Pause WHMCS lifecycle/admin edits and leave automatic Matomo creation unconfigured.
DROP PROCEDURE IF EXISTS moddhosting_import_matomo;
DELIMITER //
CREATE PROCEDURE moddhosting_import_matomo(IN apply_changes BOOLEAN)
BEGIN
    DECLARE problem_count BIGINT DEFAULT 0;
    DECLARE EXIT HANDLER FOR SQLEXCEPTION
    BEGIN
        ROLLBACK;
        RESIGNAL;
    END;

    -- Transaction safety requires the existing WHMCS/module tables to be InnoDB.
    SELECT COUNT(*) INTO problem_count
    FROM information_schema.tables
    WHERE table_schema = DATABASE()
      AND table_name IN ('tblhosting', 'tblproducts', 'mod_moddhosting_services')
      AND engine = 'InnoDB';
    IF problem_count <> 3 THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Import requires all three target tables to use InnoDB';
    END IF;

    DROP TEMPORARY TABLE IF EXISTS moddhosting_matomo_checked;
    CREATE TEMPORARY TABLE moddhosting_matomo_checked (
        input_row INT UNSIGNED PRIMARY KEY,
        service_id INT UNSIGNED NULL,
        matomo_id INT UNSIGNED NULL
    ) ENGINE=InnoDB;
    -- Validate as text before casting, including numbers too large for BIGINT.
    INSERT INTO moddhosting_matomo_checked
    SELECT input_row,
        CASE WHEN TRIM(service_id) REGEXP '^[1-9][0-9]{0,9}$'
             AND TRIM(service_id) NOT REGEXP '[^0-9]'
             AND (CHAR_LENGTH(TRIM(service_id)) < 10 OR TRIM(service_id) <= '4294967295')
             THEN CAST(TRIM(service_id) AS UNSIGNED) END,
        CASE WHEN TRIM(matomo_id) REGEXP '^[1-9][0-9]{0,9}$'
             AND TRIM(matomo_id) NOT REGEXP '[^0-9]'
             AND (CHAR_LENGTH(TRIM(matomo_id)) < 10 OR TRIM(matomo_id) <= '4294967295')
             THEN CAST(TRIM(matomo_id) AS UNSIGNED) END
    FROM moddhosting_matomo_input;
    DROP TEMPORARY TABLE IF EXISTS moddhosting_matomo_duplicates;
    CREATE TEMPORARY TABLE moddhosting_matomo_duplicates AS
        SELECT service_id FROM moddhosting_matomo_checked
        WHERE service_id IS NOT NULL GROUP BY service_id HAVING COUNT(*) > 1;

    START TRANSACTION;
    -- Hold existing mappings and WHMCS service/product rows until apply/rollback.
    SELECT h.id AS locked_service_id
    FROM tblhosting h
    JOIN moddhosting_matomo_checked c ON c.service_id = h.id
    JOIN tblproducts p ON p.id = h.packageid
    LEFT JOIN mod_moddhosting_services s ON s.service_id = h.id
    ORDER BY h.id FOR UPDATE;

    SELECT i.input_row, i.service_id, i.matomo_id,
        CASE
            WHEN c.service_id IS NULL OR c.matomo_id IS NULL THEN 'invalid positive uint32 ID'
            WHEN d.service_id IS NOT NULL THEN 'duplicate service ID in input'
            WHEN h.id IS NULL THEN 'unknown WHMCS service'
            WHEN p.id IS NULL OR p.servertype <> 'moddhosting' THEN 'product is not moddhosting'
            WHEN s.service_id IS NULL THEN 'insert'
            WHEN s.matomo_id IS NULL THEN 'fill blank'
            WHEN s.matomo_id = c.matomo_id THEN 'already matches'
            ELSE 'conflict'
        END AS disposition,
        s.matomo_id AS existing_matomo_id, h.domainstatus
    FROM moddhosting_matomo_input i
    JOIN moddhosting_matomo_checked c ON c.input_row = i.input_row
    LEFT JOIN moddhosting_matomo_duplicates d
        ON d.service_id = c.service_id
    LEFT JOIN tblhosting h ON h.id = c.service_id
    LEFT JOIN tblproducts p ON p.id = h.packageid
    LEFT JOIN mod_moddhosting_services s ON s.service_id = c.service_id
    ORDER BY i.input_row;

    -- Review duplicate Matomo IDs in the proposed result, including existing mappings.
    DROP TEMPORARY TABLE IF EXISTS moddhosting_matomo_proposed;
    CREATE TEMPORARY TABLE moddhosting_matomo_proposed AS
        SELECT s.service_id, COALESCE(s.matomo_id, c.matomo_id) AS matomo_id
        FROM mod_moddhosting_services s
        LEFT JOIN moddhosting_matomo_checked c ON c.service_id = s.service_id;
    INSERT INTO moddhosting_matomo_proposed
        SELECT c.service_id, c.matomo_id FROM moddhosting_matomo_checked c
        LEFT JOIN mod_moddhosting_services s ON s.service_id = c.service_id
        WHERE s.service_id IS NULL AND c.service_id IS NOT NULL AND c.matomo_id IS NOT NULL;
    SELECT matomo_id, GROUP_CONCAT(DISTINCT service_id ORDER BY service_id) AS service_ids,
        COUNT(DISTINCT service_id) AS service_count
    FROM moddhosting_matomo_proposed
    WHERE matomo_id IS NOT NULL
    GROUP BY matomo_id HAVING COUNT(DISTINCT service_id) > 1;

    SELECT COUNT(*) INTO problem_count
    FROM moddhosting_matomo_checked c
    LEFT JOIN tblhosting h ON h.id = c.service_id
    LEFT JOIN tblproducts p ON p.id = h.packageid
    LEFT JOIN mod_moddhosting_services s ON s.service_id = c.service_id
    WHERE c.service_id IS NULL OR c.matomo_id IS NULL OR h.id IS NULL
       OR p.id IS NULL OR p.servertype <> 'moddhosting'
       OR (s.matomo_id IS NOT NULL AND s.matomo_id <> c.matomo_id);
    IF problem_count > 0 OR EXISTS (SELECT 1 FROM moddhosting_matomo_duplicates)
       OR NOT EXISTS (SELECT 1 FROM moddhosting_matomo_checked) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Import aborted: empty input, invalid/duplicate/unknown service, wrong product or conflicting Matomo ID';
    END IF;

    IF apply_changes = 1 THEN
        INSERT INTO mod_moddhosting_services (service_id, matomo_id)
        SELECT c.service_id, c.matomo_id FROM moddhosting_matomo_checked c
        LEFT JOIN mod_moddhosting_services s ON s.service_id = c.service_id
        WHERE s.service_id IS NULL OR s.matomo_id IS NULL
        ON DUPLICATE KEY UPDATE matomo_id = VALUES(matomo_id);
        SELECT ROW_COUNT() AS affected_rows;
        COMMIT;
        SELECT 'APPLIED' AS import_result;
    ELSE
        ROLLBACK;
        SELECT 'PREVIEW ONLY: no mappings changed' AS import_result;
    END IF;
END//
DELIMITER ;
-- CALL moddhosting_import_matomo(0);
-- CALL moddhosting_import_matomo(1);
-- DROP PROCEDURE moddhosting_import_matomo;
