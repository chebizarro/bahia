CREATE UNIQUE INDEX deployment_intents_release_promotion_idempotency_uq
    ON deployment_intents (
        (metadata->>'promotion_requester'),
        (metadata->>'promotion_idempotency_key')
    )
    WHERE metadata->>'release_promotion' = 'true';
