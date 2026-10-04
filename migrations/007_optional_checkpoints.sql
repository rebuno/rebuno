-- +goose Up

ALTER TABLE execution_resources DROP CONSTRAINT execution_resources_every_steps_check;
ALTER TABLE execution_resources ADD CHECK (every_steps >= 0);

-- +goose Down

UPDATE execution_resources SET every_steps = 1 WHERE every_steps = 0;
ALTER TABLE execution_resources DROP CONSTRAINT execution_resources_every_steps_check;
ALTER TABLE execution_resources ADD CHECK (every_steps > 0);
