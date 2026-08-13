//go:build !windows

package main

func isFileLockedError(error) bool {
	return false
}
