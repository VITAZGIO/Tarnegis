//go:build !linux

package panel

// newSystemStats — показаний вне Linux нет (там панель работает только в
// режиме показа, у которого свои выдуманные показания).
func newSystemStats() *StatsSampler { return nil }
