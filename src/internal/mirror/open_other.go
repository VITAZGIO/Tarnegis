//go:build !windows

package mirror

import "os"

// openShared — на других системах открытый файл и так не мешает удалению.
func openShared(name string) (*os.File, error) { return os.Open(name) }
