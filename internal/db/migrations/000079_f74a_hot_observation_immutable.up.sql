-- An UPDATE takes the hot row lock before any row trigger can acquire the
-- coordinate lock. Reject value changes instead of allowing an update to
-- alter a material predecessor after the mover has classified its successor.
CREATE FUNCTION f74a_guard_hot_observation_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW IS DISTINCT FROM OLD THEN
    RAISE EXCEPTION 'runtime observation snapshots are immutable';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER f74a_hot_observation_immutable BEFORE UPDATE ON runtime_observations
FOR EACH ROW EXECUTE FUNCTION f74a_guard_hot_observation_update();
