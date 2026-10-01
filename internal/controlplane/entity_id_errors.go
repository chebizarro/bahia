package controlplane

// ContextVMEntityIDConflictErrorCode is the JSON-RPC error code for a create
// whose client-minted id already names an entity with different content
// (domain.ErrEntityIDConflict, bahia-irsry.35). A retry with the same id and
// the same content succeeds idempotently instead.
const ContextVMEntityIDConflictErrorCode = -32010
