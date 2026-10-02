package apistore

import "sync/atomic"

// CountDecoyVerifications replaces the decoy verification with a counter for the
// length of one test, and returns the reader and the restore. No test in this
// package runs in parallel, which is what lets a package variable be swapped.
func CountDecoyVerifications() (count func() int64, restore func()) {
	var n atomic.Int64
	saved := decoyVerify
	decoyVerify = func(string) { n.Add(1) }
	return n.Load, func() { decoyVerify = saved }
}
