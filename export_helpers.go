package steward

// AcceptAllowsForTest exposes the upload accept check to the example's tests,
// which live in another module and cannot reach an unexported function.
func AcceptAllowsForTest(accept, ext string) bool { return acceptAllows(accept, ext) }
