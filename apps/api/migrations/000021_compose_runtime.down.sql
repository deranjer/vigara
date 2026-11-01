DROP TABLE stack_deployment_previews;
ALTER TABLE stack_deployments DROP COLUMN result;
ALTER TABLE stack_deployments DROP COLUMN images;
DROP TABLE stack_profile_bindings;
