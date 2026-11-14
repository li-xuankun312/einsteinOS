package kernel

// ModelDowngradeMarker prefixes the text a child returns when its conversation was
// stopped because the server switched it to a different model on the first message.
//
// Log 10/30 03:03: the web UI showed "Sonnet 5.5's safety mechanism flagged this
// message"; the server answered with a model_update (claude-sonnet-5-5 ->
// claude-sonnet-5) and the run carried on with the lesser model. The stop is
// reported through the same path as an API/network error, so the callers that
// already retry such a child in a fresh conversation (dev_from_theorems, the
// theory chunk retries) retry this one too, without counting it as a failure.
const ModelDowngradeMarker = "[ModelDowngrade]"
