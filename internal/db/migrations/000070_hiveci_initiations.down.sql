DO $$ BEGIN
    LOCK TABLE hiveci_initiations IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM hiveci_initiations) THEN
        RAISE EXCEPTION 'HiveCI initiation rollback refused: durable replay evidence remains';
    END IF;
    DROP TABLE hiveci_initiations;
END $$;
