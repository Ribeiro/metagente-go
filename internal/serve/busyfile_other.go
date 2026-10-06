//go:build !windows

package serve

// busyFile is false here: Linux and macOS open a file that another program holds, and a file
// removed while it is open is simply gone for the next one who looks.
func busyFile(error) bool { return false }
