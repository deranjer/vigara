DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM users WHERE role='restricted') THEN
        RAISE EXCEPTION 'Reassign selected-resources accounts before rolling back resource access';
    END IF;
END $$;
DROP TABLE container_log_values;
DROP TABLE user_resource_grants;
ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('viewer','operator','admin'));
