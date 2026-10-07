package handlers

// Secret create/update/delete go through intent publishing (30900); the
// SecretHandler serves only the List method. Mutation behavior is tested
// in secret_intent_handler_test.go.
