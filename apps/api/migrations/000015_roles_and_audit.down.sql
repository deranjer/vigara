DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM users WHERE role <> 'admin') THEN
        RAISE EXCEPTION 'cannot downgrade: remove or promote viewer and operator users first';
    END IF;
END $$;

DROP TABLE audit_events;
ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('admin'));
