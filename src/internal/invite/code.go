package invite

// Короткий код приглашения (ТЗ 54): «K7HQ-3MVX-9KPL-2WR8-NZC4» вместо
// длинной строки «sshtun1:…». Код несёт только адрес сервера и 56-битный
// секрет; ключ хоста, срок и имя программа получает от панели после PAKE
// (pake.go), который доказывает обеим сторонам знание секрета.
//
// Алфавит — Crockford Base32 (0-9 A-Z без I L O U), регистр не важен,
// при вводе O→0, I/L→1. Группы по 4 через «-».
//
// Биты (старший первым, по 5 на символ):
//
//	тип(4) | секрет(56) | тело (замаскировано) | нули до кратного 20 | проверка(8)
//
// Тип — первые 4 бита: по первому символу программа знает тип и длину.
// Тело по типам — см. codeV4…; 6..15 — запас.
//
// Маска: тело XOR с потоком SHA-256 от секрета — в коде не читается ни IP,
// ни имя. Это не тайна от того, у кого код: у него секрет, он снимет маску.
// Это только чтобы адрес не бросался в глаза в переписке и на снимке экрана.
//
// Проверка — взвешенная сумма значений символов по модулю 251 (простое > 32,
// веса разные и ненулевые): ловит любую замену одного символа и любую
// перестановку двух. Считается по видимым символам, а не по снятому с маски
// телу: ошибка в секрете меняет маску, и сумма по телу ловила бы её лишь
// с вероятностью 255/256.

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// CodePrefix — ссылка и QR: «tarnegis:» + код.
const CodePrefix = "tarnegis:"

// SecretLen — длина секрета кода в байтах (56 бит).
const SecretLen = 7

const (
	codeV4         = 0 // IPv4, порт 22 — 20 символов
	codeV4Port     = 1 // IPv4 + порт — 24
	codeDomain     = 2 // домен, порт 22 — длина(6) + символы по 6 бит
	codeV6         = 3 // IPv6, порт 22 — 40
	codeV6Port     = 4 // IPv6 + порт — 44
	codeDomainPort = 5 // домен + порт
)

const (
	codeAlphabet  = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	domainCharset = "abcdefghijklmnopqrstuvwxyz0123456789-."
	maxCodeDomain = 63
	headBits      = 4 + SecretLen*8
)

// ErrCodeTypo — полный код с неверной проверкой.
var ErrCodeTypo = errors.New("код введён с ошибкой — проверь символы")

// Code — разобранный короткий код.
type Code struct {
	Host   string
	Port   int
	Secret [SecretLen]byte
}

// Tag — короткая метка кода (2 hex, 8 бит хеша секрета): по ней панель
// находит, о каком из открытых приглашений речь, не получая секрета.
// Панель следит, чтобы у открытых приглашений метки не совпадали.
func (c Code) Tag() string { return CodeTag(c.Secret) }

// CodeTag — метка секрета (см. Code.Tag).
func CodeTag(secret [SecretLen]byte) string {
	sum := sha256.Sum256(append([]byte("tarnegis code tag v1\x00"), secret[:]...))
	return fmt.Sprintf("%02x", sum[0])
}

// EncodeCode — код с дефисами (так его выдаёт панель). Домен длиннее 63
// или с непонятными символами — ошибка: тогда остаётся длинная строка.
func EncodeCode(host string, port int, secret [SecretLen]byte) (string, error) {
	if port <= 0 {
		port = 22
	}
	if port > 65535 {
		return "", errors.New("порт вне 1..65535")
	}
	var w bitWriter
	var body bitWriter
	typ := 0
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		ip = ip.Unmap()
		if ip.Is4() {
			typ = codeV4
			a := ip.As4()
			body.bytes(a[:])
		} else {
			typ = codeV6
			a := ip.As16()
			body.bytes(a[:])
		}
		if port != 22 {
			typ++ // V4Port / V6Port
			body.put(uint64(port), 16)
		}
	} else {
		d := strings.ToLower(strings.TrimSuffix(host, "."))
		if d == "" || len(d) > maxCodeDomain {
			return "", errors.New("домен не помещается в короткий код")
		}
		typ = codeDomain
		if port != 22 {
			typ = codeDomainPort
		}
		body.put(uint64(len(d)), 6)
		for i := 0; i < len(d); i++ {
			k := strings.IndexByte(domainCharset, d[i])
			if k < 0 {
				return "", fmt.Errorf("символ %q не помещается в короткий код", d[i])
			}
			body.put(uint64(k), 6)
		}
		if port != 22 {
			body.put(uint64(port), 16)
		}
	}
	w.put(uint64(typ), 4)
	w.bytes(secret[:])
	mask := maskStream(secret, body.n)
	for i := 0; i < body.n; i++ {
		w.bit(body.at(i) ^ mask.at(i))
	}
	total := codeChars(headBits + body.n)
	for w.n < total*5-8 {
		w.bit(0)
	}
	w.put(0, 8)
	chars := w.chars()
	c := checkValue(chars)
	chars[len(chars)-2] |= byte(c >> 5)
	chars[len(chars)-1] = byte(c & 31)
	return formatCode(chars), nil
}

// codeChars — сколько символов в коде с dataBits бит до проверки.
func codeChars(dataBits int) int {
	bits := dataBits + 8
	groups := (bits + 19) / 20
	return groups * 4
}

// checkValue — значение проверки (0..250) для символов с нулевыми битами
// проверки: сумма по всем символам станет ≡ 0 (mod 251).
func checkValue(chars []byte) int {
	s := checkSum(chars)
	return (251 - s) % 251
}

func checkSum(chars []byte) int {
	n := len(chars)
	s := 0
	for i, c := range chars {
		w := i + 33
		switch i {
		case n - 1:
			w = 1
		case n - 2:
			w = 32
		}
		s = (s + w*int(c)) % 251
	}
	return s
}

func formatCode(chars []byte) string {
	var b strings.Builder
	for i, c := range chars {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(codeAlphabet[c])
	}
	return b.String()
}

// NormalizeCode — символы кода без префикса, дефисов, пробелов, в верхнем
// регистре, O→0, I/L→1. ok=false — в тексте есть то, чего в коде не бывает.
func NormalizeCode(text string) (string, bool) {
	s := strings.TrimSpace(text)
	if len(s) >= len(CodePrefix) && strings.EqualFold(s[:len(CodePrefix)], CodePrefix) {
		s = s[len(CodePrefix):]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '-' || r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '/':
			continue
		case r >= 'a' && r <= 'z':
			r -= 'a' - 'A'
		}
		switch r {
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		if r > 127 || strings.IndexRune(codeAlphabet, r) < 0 {
			return "", false
		}
		b.WriteRune(r)
	}
	return b.String(), true
}

// LooksLikeCode — текст похож на короткий код (или его начало, с «tarnegis:»).
func LooksLikeCode(text string) bool {
	t := strings.TrimSpace(text)
	if len(t) >= len(CodePrefix) && strings.EqualFold(t[:len(CodePrefix)], CodePrefix) {
		return true
	}
	n, ok := NormalizeCode(t)
	return ok && len(n) >= 20
}

// CodeLength — сколько символов должно быть в коде, начало которого norm
// (после NormalizeCode). 0 — пока не понять (домен: длина под маской,
// нужно 14 символов); ошибка — такого типа нет.
func CodeLength(norm string) (int, error) {
	if norm == "" {
		return 0, nil
	}
	vals := make([]byte, len(norm))
	for i := 0; i < len(norm); i++ {
		vals[i] = byte(strings.IndexByte(codeAlphabet, norm[i]))
	}
	r := bitReader{chars: vals}
	typ := int(r.get(0, 4))
	switch typ {
	case codeV4:
		return codeChars(headBits + 32), nil
	case codeV4Port:
		return codeChars(headBits + 48), nil
	case codeV6:
		return codeChars(headBits + 128), nil
	case codeV6Port:
		return codeChars(headBits + 144), nil
	case codeDomain, codeDomainPort:
		if len(vals)*5 < headBits+6 {
			return 0, nil
		}
		var secret [SecretLen]byte
		for i := range secret {
			secret[i] = byte(r.get(4+i*8, 8))
		}
		l := int(r.get(headBits, 6) ^ maskStream(secret, 6).get(0, 6))
		if l == 0 {
			return 0, ErrCodeTypo
		}
		extra := 0
		if typ == codeDomainPort {
			extra = 16
		}
		return codeChars(headBits + 6 + 6*l + extra), nil
	}
	return 0, errors.New("код другого вида — обнови программу")
}

// ParseCode разбирает короткий код (с дефисами или без, с «tarnegis:»).
func ParseCode(text string) (Code, error) {
	norm, ok := NormalizeCode(text)
	if !ok || norm == "" {
		return Code{}, ErrNotInvite
	}
	want, err := CodeLength(norm)
	if err != nil {
		return Code{}, err
	}
	if want == 0 || len(norm) < want {
		return Code{}, fmt.Errorf("код не полный: ещё %d символов", max(want-len(norm), 1))
	}
	if len(norm) > want {
		return Code{}, ErrCodeTypo
	}
	vals := make([]byte, len(norm))
	for i := 0; i < len(norm); i++ {
		vals[i] = byte(strings.IndexByte(codeAlphabet, norm[i]))
	}
	if checkSum(vals) != 0 {
		return Code{}, ErrCodeTypo
	}
	r := bitReader{chars: vals}
	typ := int(r.get(0, 4))
	var c Code
	for i := range c.Secret {
		c.Secret[i] = byte(r.get(4+i*8, 8))
	}
	bodyBits := want*5 - 8 - headBits
	mask := maskStream(c.Secret, bodyBits)
	pos := headBits
	next := func(n int) uint64 {
		v := r.get(pos, n) ^ mask.get(pos-headBits, n)
		pos += n
		return v
	}
	c.Port = 22
	switch typ {
	case codeV4, codeV4Port:
		var a [4]byte
		for i := range a {
			a[i] = byte(next(8))
		}
		c.Host = netip.AddrFrom4(a).String()
	case codeV6, codeV6Port:
		var a [16]byte
		for i := range a {
			a[i] = byte(next(8))
		}
		c.Host = netip.AddrFrom16(a).String()
	case codeDomain, codeDomainPort:
		l := int(next(6))
		var b strings.Builder
		for i := 0; i < l; i++ {
			k := int(next(6))
			if k >= len(domainCharset) {
				return Code{}, ErrCodeTypo
			}
			b.WriteByte(domainCharset[k])
		}
		c.Host = b.String()
	}
	if typ == codeV4Port || typ == codeV6Port || typ == codeDomainPort {
		c.Port = int(next(16))
		if c.Port == 0 {
			return Code{}, ErrCodeTypo
		}
	}
	return c, nil
}

// maskStream — n бит маски от секрета: SHA-256(метка, секрет, счётчик).
func maskStream(secret [SecretLen]byte, n int) bitWriter {
	var w bitWriter
	for ctr := uint32(0); w.n < n; ctr++ {
		h := sha256.New()
		h.Write([]byte("tarnegis code mask v1\x00"))
		h.Write(secret[:])
		binary.Write(h, binary.BigEndian, ctr)
		w.bytes(h.Sum(nil))
	}
	return w
}

// bitWriter — биты подряд, старший первым.
type bitWriter struct {
	b []byte // по биту в байте: просто и коротко, коды маленькие
	n int
}

func (w *bitWriter) bit(v byte) { w.b = append(w.b, v&1); w.n++ }
func (w *bitWriter) put(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		w.bit(byte(v >> uint(i)))
	}
}
func (w *bitWriter) bytes(p []byte) {
	for _, x := range p {
		w.put(uint64(x), 8)
	}
}
func (w bitWriter) at(i int) byte { return w.b[i] }
func (w bitWriter) get(pos, n int) uint64 {
	var v uint64
	for i := 0; i < n; i++ {
		v = v<<1 | uint64(w.b[pos+i])
	}
	return v
}
func (w *bitWriter) chars() []byte {
	out := make([]byte, (w.n+4)/5)
	for i := 0; i < w.n; i++ {
		out[i/5] |= w.b[i] << uint(4-i%5)
	}
	return out
}

// bitReader — биты из значений символов (по 5).
type bitReader struct{ chars []byte }

func (r bitReader) get(pos, n int) uint64 {
	var v uint64
	for i := pos; i < pos+n; i++ {
		v = v<<1 | uint64(r.chars[i/5]>>uint(4-i%5)&1)
	}
	return v
}
