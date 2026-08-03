package release

// setKey swaps the verifying key for the duration of a test.
//
// In a _test.go file so it cannot exist in a shipped binary: the compiled-in
// key being unreachable from outside this package is part of what the key is
// for, and a production setter would quietly undo that.
func setKey(pemText string) { activeKey = pemText }
