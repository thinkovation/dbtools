DROP SCHEMA IF EXISTS dbinspect_test CASCADE;
CREATE SCHEMA dbinspect_test;
SET search_path = dbinspect_test;

CREATE TABLE customers (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  email      text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE customers IS 'Paying accounts';

CREATE TABLE locations (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  customer_id bigint NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
  name        varchar(200) NOT NULL,
  crm_id      text
);
-- deliberately no index on locations.customer_id

CREATE TABLE location_settings (
  location_id bigint PRIMARY KEY REFERENCES locations(id),
  timezone    text
);

CREATE TABLE audit_log (
  happened_at timestamptz NOT NULL,
  payload     jsonb
);
CREATE INDEX audit_a ON audit_log (happened_at);
CREATE INDEX audit_b ON audit_log (happened_at);

CREATE VIEW active_locations AS SELECT * FROM locations;
