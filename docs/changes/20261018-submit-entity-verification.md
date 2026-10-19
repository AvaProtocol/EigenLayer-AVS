# Verify the signed validation entity at grant submit

Prepare still allocates conservatively, skipping entities whose occupancy cannot
be read. Submit now checks only the entity in the owner's signed grant. A free
entity is retained even if an earlier candidate becomes available again. A stored
or confirmed on-chain occupant remains `409 POLICIES_ENTITY_TAKEN`.

A failed occupancy read is `503 POLICIES_ENTITY_UNVERIFIED`. No new grant is
stored, and the client can retry submit with the same signature while its signing
window remains valid. Submit logs the refusal detail at WARN without logging the
signature. Permission reconstruction, signature verification, coverage checks and
the runner lock remain in place.
