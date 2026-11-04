ALTER TABLE stack_bindings
    ADD COLUMN recipients JSONB NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(recipients) = 'array'),
    ADD COLUMN imported BOOLEAN NOT NULL DEFAULT false;
