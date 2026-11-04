-- DESTRUCTIVE FIXTURE: run only in an empty disposable database, from repo root.
CREATE TABLE tblproducts (id INT UNSIGNED PRIMARY KEY, servertype VARCHAR(64)) ENGINE=InnoDB;
CREATE TABLE tblhosting (id INT UNSIGNED PRIMARY KEY, packageid INT UNSIGNED, domainstatus VARCHAR(32)) ENGINE=InnoDB;
CREATE TABLE mod_moddhosting_services (
    service_id INT UNSIGNED PRIMARY KEY,
    image_version VARCHAR(128) NOT NULL DEFAULT '',
    staging_label VARCHAR(32) NOT NULL DEFAULT '',
    matomo_id INT UNSIGNED NULL,
    matomo_attempt VARCHAR(64) NULL
) ENGINE=InnoDB;
INSERT INTO tblproducts VALUES (1, 'moddhosting'), (2, 'other');
INSERT INTO tblhosting VALUES (1,1,'Active'), (2,1,'Suspended'), (3,1,'Terminated'), (4,2,'Active'), (5,1,'Active');
INSERT INTO mod_moddhosting_services VALUES (2,'v1','stage',NULL,'attempt'), (3,'v2','',30,NULL);
SOURCE whmcs-plugin/sql/matomo-stage.sql;
SOURCE whmcs-plugin/sql/matomo-import.sql;
DELIMITER //
CREATE PROCEDURE assert_rejected(IN service_text LONGTEXT, IN matomo_text LONGTEXT)
BEGIN
    DECLARE rejected BOOLEAN DEFAULT FALSE;
    DELETE FROM moddhosting_matomo_input;
    INSERT INTO moddhosting_matomo_input (service_id,matomo_id) VALUES ('5','50'), (service_text,matomo_text);
    BEGIN
        DECLARE CONTINUE HANDLER FOR SQLSTATE '45000' SET rejected = TRUE;
        CALL moddhosting_import_matomo(1);
    END;
    IF NOT rejected OR EXISTS (SELECT 1 FROM mod_moddhosting_services WHERE service_id = 5) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'FAIL: invalid import was not completely rejected';
    END IF;
END//
CREATE PROCEDURE run_matomo_smoke()
BEGIN
    INSERT INTO moddhosting_matomo_input (service_id,matomo_id) VALUES ('1','10'),('2','20'),('3','30');
    CALL moddhosting_import_matomo(0);
    IF EXISTS (SELECT 1 FROM mod_moddhosting_services WHERE service_id = 1 OR (service_id = 2 AND matomo_id IS NOT NULL)) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'FAIL: preview changed mappings';
    END IF;
    CALL moddhosting_import_matomo(1);
    CALL moddhosting_import_matomo(1);
    IF (SELECT COUNT(*) FROM mod_moddhosting_services WHERE matomo_id = service_id * 10) <> 3
       OR NOT EXISTS (SELECT 1 FROM mod_moddhosting_services
           WHERE service_id = 2 AND image_version = 'v1' AND staging_label = 'stage' AND matomo_attempt = 'attempt') THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'FAIL: apply, repeat or field preservation';
    END IF;
    CALL assert_rejected('3','31');
    CALL assert_rejected('5','51');
    CALL assert_rejected('999','99');
    CALL assert_rejected('4','40');
    CALL assert_rejected('0','40');
    CALL assert_rejected('-1','40');
    CALL assert_rejected('1.5','40');
    CALL assert_rejected('4294967296','40');
    CALL assert_rejected('999999999999999999999999999999','40');
    CALL assert_rejected('1','4294967296');
    CALL assert_rejected('1','0');
    CALL assert_rejected('1',NULL);
    CALL assert_rejected('1','2x');
    CALL assert_rejected('1','');
    CALL assert_rejected(CONCAT('1',CHAR(10)),'10');
    DELETE FROM moddhosting_matomo_input;
    INSERT INTO moddhosting_matomo_input (service_id,matomo_id) VALUES ('5','10');
    CALL moddhosting_import_matomo(1);
    IF NOT EXISTS (SELECT 1 FROM mod_moddhosting_services WHERE service_id = 5 AND matomo_id = 10) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'FAIL: duplicate Matomo ID should be reviewable, not rejected';
    END IF;
    SELECT 'PASS: Matomo import smoke checks' AS result;
END//
DELIMITER ;
CALL run_matomo_smoke();
DROP PROCEDURE run_matomo_smoke;
DROP PROCEDURE assert_rejected;
DROP PROCEDURE moddhosting_import_matomo;
