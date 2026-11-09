package weburl

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// parseHost tolkar värden: IPv6 inom hakparenteser, en ogenomskinlig värd för scheman som inte är
// speciella, och annars ett domännamn (med IDN som Punycode) eller en IPv4-adress.
func parseHost(input string, notSpecial bool) (string, error) {
	if strings.HasPrefix(input, "[") {
		if !strings.HasSuffix(input, "]") {
			return "", ErrInvalid
		}
		addr, ok := parseIPv6(input[1 : len(input)-1])
		if !ok {
			return "", ErrInvalid
		}
		return "[" + serializeIPv6(addr) + "]", nil
	}
	if notSpecial {
		for _, r := range input {
			if isForbiddenHost(r) {
				return "", ErrInvalid
			}
		}
		return percentEncodeString(input, inC0ControlSet), nil
	}
	domain := strings.ToValidUTF8(string(PercentDecode(input)), "\uFFFD")
	ascii, ok := domainToASCII(domain)
	if !ok {
		return "", ErrInvalid
	}
	for _, r := range ascii {
		if isForbiddenHost(r) || r < 0x20 || r == '%' || r == 0x7F {
			return "", ErrInvalid
		}
	}
	if endsInNumber(ascii) {
		v, ok := parseIPv4(ascii)
		if !ok {
			return "", ErrInvalid
		}
		return serializeIPv4(v), nil
	}
	return ascii, nil
}

func isForbiddenHost(r rune) bool {
	switch r {
	case 0, '\t', '\n', '\r', ' ', '#', '/', ':', '<', '>', '?', '@', '[', '\\', ']', '^', '|':
		return true
	}
	return false
}

// domainToASCII gör domänen till gemener och varje etikett med andra tecken än ASCII till Punycode
// (xn--). Det är en förenkling av UTS #46: normalisering (NFC) och de ovanligare
// teckenmappningarna görs inte, eftersom Go:s standardbibliotek saknar tabellerna för dem.
func domainToASCII(domain string) (string, bool) {
	domain = strings.Map(func(r rune) rune {
		switch r {
		case 0x3002, 0xFF0E, 0xFF61: // punkter i andra skriftsystem
			return '.'
		case 0xAD, 0x200B, 0x2060, 0xFEFF: // tecken som UTS #46 tar bort
			return -1
		}
		return unicode.ToLower(r)
	}, domain)
	labels := strings.Split(domain, ".")
	for i, label := range labels {
		if isASCII(label) {
			continue
		}
		encoded, ok := punycode(label)
		if !ok {
			return "", false
		}
		labels[i] = "xn--" + encoded
	}
	out := strings.Join(labels, ".")
	if out == "" {
		return "", false
	}
	return out, true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// punycode kodar en etikett enligt RFC 3492.
func punycode(s string) (string, bool) {
	const (
		base, tmin, tmax, skew, damp = 36, 1, 26, 38, 700
		initialBias, initialN        = 72, 128
	)
	runes := []rune(s)
	var out []byte
	for _, r := range runes {
		if r < 0x80 {
			out = append(out, byte(r))
		}
	}
	b := len(out)
	h := b
	if b > 0 {
		out = append(out, '-')
	}
	digit := func(d int) byte {
		if d < 26 {
			return byte('a' + d)
		}
		return byte('0' + d - 26)
	}
	adapt := func(delta, numPoints int, first bool) int {
		if first {
			delta /= damp
		} else {
			delta /= 2
		}
		delta += delta / numPoints
		k := 0
		for delta > ((base-tmin)*tmax)/2 {
			delta /= base - tmin
			k += base
		}
		return k + (base-tmin+1)*delta/(delta+skew)
	}
	n, delta, bias := initialN, 0, initialBias
	for h < len(runes) {
		m := int(utf8.MaxRune) + 1
		for _, r := range runes {
			if int(r) >= n && int(r) < m {
				m = int(r)
			}
		}
		delta += (m - n) * (h + 1)
		n = m
		for _, r := range runes {
			if int(r) < n {
				delta++
			}
			if int(r) == n {
				q := delta
				for k := base; ; k += base {
					t := k - bias
					if t < tmin {
						t = tmin
					} else if t > tmax {
						t = tmax
					}
					if q < t {
						break
					}
					out = append(out, digit(t+(q-t)%(base-t)))
					q = (q - t) / (base - t)
				}
				out = append(out, digit(q))
				bias = adapt(delta, h+1, h == b)
				delta = 0
				h++
			}
		}
		delta++
		n++
	}
	return string(out), true
}

func endsInNumber(s string) bool {
	parts := strings.Split(s, ".")
	if parts[len(parts)-1] == "" {
		if len(parts) == 1 {
			return false
		}
		parts = parts[:len(parts)-1]
	}
	last := parts[len(parts)-1]
	if last != "" && strings.Trim(last, "0123456789") == "" {
		return true
	}
	_, ok := parseIPv4Number(last)
	return ok
}

func parseIPv4Number(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	base := 10
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		s, base = s[2:], 16
	} else if len(s) >= 2 && s[0] == '0' {
		s, base = s[1:], 8
	}
	if s == "" {
		return 0, true
	}
	v, err := strconv.ParseUint(s, base, 64)
	if err != nil {
		// För stora tal räknas som för stora, inte som fel.
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return 1 << 40, true
		}
		return 0, false
	}
	return v, true
}

func parseIPv4(s string) (uint32, bool) {
	parts := strings.Split(s, ".")
	if parts[len(parts)-1] == "" && len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 4 {
		return 0, false
	}
	nums := make([]uint64, len(parts))
	for i, p := range parts {
		v, ok := parseIPv4Number(p)
		if !ok {
			return 0, false
		}
		nums[i] = v
	}
	for _, v := range nums[:len(nums)-1] {
		if v > 255 {
			return 0, false
		}
	}
	last := nums[len(nums)-1]
	if last >= 1<<(8*(5-len(nums))) {
		return 0, false
	}
	ipv4 := last
	for i, v := range nums[:len(nums)-1] {
		ipv4 += v << (8 * (3 - i))
	}
	return uint32(ipv4), true
}

func serializeIPv4(v uint32) string {
	return strconv.Itoa(int(v>>24)) + "." + strconv.Itoa(int(v>>16&255)) + "." + strconv.Itoa(int(v>>8&255)) + "." + strconv.Itoa(int(v&255))
}

func parseIPv6(s string) ([8]uint16, bool) {
	var addr [8]uint16
	in := []rune(s)
	at := func(i int) rune {
		if i < len(in) {
			return in[i]
		}
		return eof
	}
	piece, compress, p := 0, -1, 0
	if at(p) == ':' {
		if at(p+1) != ':' {
			return addr, false
		}
		p += 2
		piece++
		compress = piece
	}
	for at(p) != eof {
		if piece == 8 {
			return addr, false
		}
		if at(p) == ':' {
			if compress != -1 {
				return addr, false
			}
			p++
			piece++
			compress = piece
			continue
		}
		value, length := 0, 0
		for length < 4 && isASCIIHex(at(p)) {
			d, _ := strconv.ParseUint(string(at(p)), 16, 8)
			value = value*16 + int(d)
			p++
			length++
		}
		if at(p) == '.' {
			if length == 0 {
				return addr, false
			}
			p -= length
			if piece > 6 {
				return addr, false
			}
			seen := 0
			for at(p) != eof {
				ipv4 := -1
				if seen > 0 {
					if at(p) == '.' && seen < 4 {
						p++
					} else {
						return addr, false
					}
				}
				if !isASCIIDigit(at(p)) {
					return addr, false
				}
				for isASCIIDigit(at(p)) {
					n := int(at(p) - '0')
					if ipv4 == -1 {
						ipv4 = n
					} else if ipv4 == 0 {
						return addr, false
					} else {
						ipv4 = ipv4*10 + n
					}
					if ipv4 > 255 {
						return addr, false
					}
					p++
				}
				addr[piece] = addr[piece]*0x100 + uint16(ipv4)
				seen++
				if seen == 2 || seen == 4 {
					piece++
				}
			}
			if seen != 4 {
				return addr, false
			}
			break
		} else if at(p) == ':' {
			p++
			if at(p) == eof {
				return addr, false
			}
		} else if at(p) != eof {
			return addr, false
		}
		addr[piece] = uint16(value)
		piece++
	}
	if compress != -1 {
		swaps := piece - compress
		piece = 7
		for piece != 0 && swaps > 0 {
			addr[piece], addr[compress+swaps-1] = addr[compress+swaps-1], addr[piece]
			piece--
			swaps--
		}
	} else if piece != 8 {
		return addr, false
	}
	return addr, true
}

func serializeIPv6(addr [8]uint16) string {
	// Den längsta följden av minst två nollor skrivs som ::.
	best, bestLen := -1, 1
	for i := 0; i < 8; {
		if addr[i] != 0 {
			i++
			continue
		}
		j := i
		for j < 8 && addr[j] == 0 {
			j++
		}
		if j-i > bestLen {
			best, bestLen = i, j-i
		}
		i = j
	}
	var b strings.Builder
	ignore0 := false
	for i := 0; i < 8; i++ {
		if ignore0 && addr[i] == 0 {
			continue
		}
		ignore0 = false
		if i == best {
			if i == 0 {
				b.WriteString("::")
			} else {
				b.WriteString(":")
			}
			ignore0 = true
			continue
		}
		b.WriteString(strconv.FormatUint(uint64(addr[i]), 16))
		if i != 7 {
			b.WriteByte(':')
		}
	}
	return b.String()
}
