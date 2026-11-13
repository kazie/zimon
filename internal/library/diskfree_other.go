//go:build !unix

package library

func freeBytes(string) (int64, bool) { return 0, false }
