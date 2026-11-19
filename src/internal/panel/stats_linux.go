//go:build linux

package panel

import (
	"os"
	"strconv"
	"syscall"
)

// procSource — показания из настоящих /proc и statfs.
type procSource struct{}

func (procSource) read(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}

func (procSource) pids() []int {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	out := make([]int, 0, len(ents))
	for _, e := range ents {
		if n, err := strconv.Atoi(e.Name()); err == nil && e.IsDir() {
			out = append(out, n)
		}
	}
	return out
}

func (procSource) statfs(mount string) (total, free uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(mount, &st); err != nil {
		return 0, 0, false
	}
	bs := uint64(st.Bsize)
	// Свободно для обычных программ (Bavail), как в df: зарезервированное
	// для root считается занятым.
	return st.Blocks * bs, st.Bavail * bs, true
}

// newSystemStats — замеры этой машины.
func newSystemStats() *StatsSampler {
	return newStatsSampler(procSource{}, uint64(os.Getpagesize()))
}
