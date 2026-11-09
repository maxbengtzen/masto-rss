package js

import (
	"math"
	"time"
	"unicode/utf16"
)

// ParseDate är Date.parse i V8 (Node): först ES-formatet (ISO 8601), sedan V8:s förlåtande äldre
// tolkning som klarar RFC 822 och det mesta däremellan. Utan tidszon gäller lokal tid (TZ), utom
// för ett ISO-datum utan klockslag, som är UTC. Andra värdet är false där JavaScript ger NaN.
//
// Det här är en översättning av V8:s dateparser-inl.h, så att flöden med udda datum tolkas
// likadant som förut.
func ParseDate(s string) (int64, bool) {
	sc := &dateScanner{in: utf16.Encode([]rune(s))}
	sc.next = sc.scan()
	var day dayComposer
	var tm timeComposer
	tz := tzComposer{sign: none, hour: none, minute: none}
	tm.hourOffset = none
	day.namedMonth = none

	tok := parseES5(sc, &day, &tm, &tz)
	if tok.kind == tokInvalid {
		return 0, false
	}
	hasReadNumber := day.index > 0
	for ; tok.kind != tokEnd; tok = sc.Next() {
		switch {
		case tok.kind == tokNumber:
			hasReadNumber = true
			n := tok.value
			if sc.SkipSymbol(':') {
				if sc.SkipSymbol(':') {
					// n + "::"
					if tm.index != 0 {
						return 0, false
					}
					tm.add(n)
					tm.add(0)
				} else {
					// n + ":"
					if !tm.add(n) {
						return 0, false
					}
					if sc.Peek().isSymbol('.') {
						sc.Next()
					}
				}
			} else if sc.SkipSymbol('.') && tm.isExpecting(n) {
				tm.add(n)
				if sc.Peek().kind != tokNumber {
					return 0, false
				}
				ms := readMilliseconds(sc.Next())
				if ms < 0 {
					return 0, false
				}
				tm.addFinal(ms)
			} else if tz.isExpecting(n) {
				tz.minute = n
			} else if tm.isExpecting(n) {
				tm.addFinal(n)
				// Efter ett klockslag måste det komma slut, blanktecken, Z, + eller -.
				p := sc.Peek()
				if p.kind != tokEnd && p.kind != tokSpace && !p.isKeywordZ() && !p.isSign() {
					return 0, false
				}
			} else {
				if !day.add(n) {
					return 0, false
				}
				sc.SkipSymbol('-')
			}
		case tok.kind == tokKeyword:
			switch {
			case tok.keyword == kwAmPm && tm.index != 0:
				tm.hourOffset = tok.value
			case tok.keyword == kwMonth:
				day.namedMonth = tok.value
				sc.SkipSymbol('-')
			case tok.keyword == kwZone && hasReadNumber:
				tz.set(tok.value)
			default:
				// Okända ord går bra före första talet, men inte direkt intill det.
				if hasReadNumber || sc.Peek().kind == tokNumber {
					return 0, false
				}
			}
		case tok.isSign() && (tz.isUTC() || tm.index != 0):
			// Tidszon efter UTC/GMT eller efter ett klockslag.
			tz.setSign(tok.sign())
			n, length := 0, 0
			if sc.Peek().kind == tokNumber {
				t := sc.Next()
				n, length = t.value, t.length
			}
			hasReadNumber = true
			switch {
			case sc.Peek().isSymbol(':'):
				tz.hour = n
				tz.minute = none
			case length == 1 || length == 2:
				tz.hour, tz.minute = n, 0
			case length == 3 || length == 4:
				tz.hour, tz.minute = n/100, n%100
			default:
				return 0, false
			}
		case (tok.isSign() || tok.isSymbol(')')) && hasReadNumber:
			return 0, false
		}
	}

	year, month, d, ok := day.write()
	if !ok {
		return 0, false
	}
	hour, minute, second, ms, ok := tm.write()
	if !ok {
		return 0, false
	}
	offset, local, ok := tz.write()
	if !ok {
		return 0, false
	}
	return makeDate(year, month, d, hour, minute, second, ms, offset, local)
}

const none = math.MaxInt32

// makeDate räknar fram millisekunder sedan 1970 som MakeDay/MakeTime/UTC i ECMAScript.
func makeDate(year, month, day, hour, minute, second, ms, offset int, local bool) (int64, bool) {
	// Månader utanför 0–11 och dagar utanför månaden rullar över, som i JavaScript.
	y := int64(year) + int64(month)/12
	m := month % 12
	days := daysFromCivil(y, m+1, 1) + int64(day) - 1
	t := days*86_400_000 + int64(hour)*3_600_000 + int64(minute)*60_000 + int64(second)*1000 + int64(ms)
	if local {
		if y < -200000 || y > 300000 {
			return 0, false
		}
		t = localToUTC(t)
	} else {
		t -= int64(offset) * 1000
	}
	if t > 8.64e15 || t < -8.64e15 {
		return 0, false
	}
	return t, true
}

// localToUTC gör lokal tid (ms räknat som om den vore UTC) till UTC, som V8 med ICU: en tid som
// finns två gånger (när sommartiden slutar) blir den första, och en tid som inte finns (när den
// börjar) räknas med förskjutningen från före bytet.
func localToUTC(local int64) int64 {
	offset := func(ms int64) int64 {
		_, secs := time.UnixMilli(ms).In(time.Local).Zone()
		return int64(secs) * 1000
	}
	const day = 86_400_000
	before, after := offset(local-day), offset(local+day)
	c1, c2 := local-before, local-after
	ok1, ok2 := offset(c1) == before, offset(c2) == after
	switch {
	case ok1 && ok2:
		return min(c1, c2)
	case ok2:
		return c2
	}
	return c1
}

// daysFromCivil ger antalet dagar från 1970-01-01 till datumet (proleptisk gregoriansk kalender).
func daysFromCivil(y int64, m, d int) int64 {
	if m <= 2 {
		y--
	}
	era := y / 400
	if y < 0 && y%400 != 0 {
		era = (y - 399) / 400
	}
	yoe := y - era*400
	mp := (m + 9) % 12
	doy := int64((153*mp+2)/5 + d - 1)
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return era*146097 + doe - 719468
}

func parseES5(sc *dateScanner, day *dayComposer, tm *timeComposer, tz *tzComposer) dateToken {
	// Datum: [('-'|'+')yy]yyyy[-MM[-DD]]
	if sc.Peek().isSign() {
		sign := sc.Next()
		if !sc.Peek().isFixedNumber(6) {
			return sign
		}
		year := sc.Next().value
		if sign.sign() < 0 && year == 0 {
			return sign
		}
		day.add(sign.sign() * year)
	} else if sc.Peek().isFixedNumber(4) {
		day.add(sc.Next().value)
	} else {
		return sc.Next()
	}
	if sc.SkipSymbol('-') {
		if p := sc.Peek(); !p.isFixedNumber(2) || p.value < 1 || p.value > 12 {
			return sc.Next()
		}
		day.add(sc.Next().value)
		if sc.SkipSymbol('-') {
			if p := sc.Peek(); !p.isFixedNumber(2) || p.value < 1 || p.value > 31 {
				return sc.Next()
			}
			day.add(sc.Next().value)
		}
	}
	// Klockslag: THH:mm[:ss[.sss]] och tidszon Z eller ±hh:mm.
	if p := sc.Peek(); !(p.kind == tokKeyword && p.keyword == kwTimeSep) {
		if p.kind != tokEnd {
			return sc.Next()
		}
	} else {
		sc.Next()
		if p := sc.Peek(); !p.isFixedNumber(2) || p.value > 24 {
			return dateToken{kind: tokInvalid}
		}
		hourIs24 := sc.Peek().value == 24
		tm.add(sc.Next().value)
		if !sc.SkipSymbol(':') {
			return dateToken{kind: tokInvalid}
		}
		if p := sc.Peek(); !p.isFixedNumber(2) || p.value > 59 || hourIs24 && p.value > 0 {
			return dateToken{kind: tokInvalid}
		}
		tm.add(sc.Next().value)
		if sc.SkipSymbol(':') {
			if p := sc.Peek(); !p.isFixedNumber(2) || p.value > 59 || hourIs24 && p.value > 0 {
				return dateToken{kind: tokInvalid}
			}
			tm.add(sc.Next().value)
			if sc.SkipSymbol('.') {
				if p := sc.Peek(); p.kind != tokNumber || hourIs24 && p.value > 0 {
					return dateToken{kind: tokInvalid}
				}
				tm.add(readMilliseconds(sc.Next()))
			}
		}
		if sc.Peek().isKeywordZ() {
			sc.Next()
			tz.set(0)
		} else if p := sc.Peek(); p.isSymbol('+') || p.isSymbol('-') {
			tz.setSign(sc.Next().sign())
			if sc.Peek().isFixedNumber(4) {
				hm := sc.Next().value
				hour, minute := hm/100, hm%100
				if hour > 23 || minute > 59 {
					return dateToken{kind: tokInvalid}
				}
				tz.hour, tz.minute = hour, minute
			} else {
				if p := sc.Peek(); !p.isFixedNumber(2) || p.value > 23 {
					return dateToken{kind: tokInvalid}
				}
				tz.hour = sc.Next().value
				if !sc.SkipSymbol(':') {
					return dateToken{kind: tokInvalid}
				}
				if p := sc.Peek(); !p.isFixedNumber(2) || p.value > 59 {
					return dateToken{kind: tokInvalid}
				}
				tz.minute = sc.Next().value
			}
		}
		if sc.Peek().kind != tokEnd {
			return dateToken{kind: tokInvalid}
		}
	}
	// Ett ISO-datum utan klockslag och tidszon är UTC, med klockslag lokal tid.
	if tz.hour == none && tm.index == 0 {
		tz.set(0)
	}
	day.iso = true
	return dateToken{kind: tokEnd}
}

func readMilliseconds(t dateToken) int {
	n, length := t.value, t.length
	switch {
	case length == 1:
		n *= 100
	case length == 2:
		n *= 10
	case length > 3:
		if length > 9 {
			length = 9
		}
		factor := 1
		for ; length > 3; length-- {
			factor *= 10
		}
		n /= factor
	}
	return n
}

type dayComposer struct {
	comp       [3]int
	index      int
	namedMonth int
	iso        bool
}

func (d *dayComposer) add(n int) bool {
	if d.index == len(d.comp) {
		return false
	}
	d.comp[d.index] = n
	d.index++
	return true
}

func isDay(n int) bool { return n >= 1 && n <= 31 }

func (d *dayComposer) write() (year, month, day int, ok bool) {
	if d.index < 1 {
		return 0, 0, 0, false
	}
	for d.index < len(d.comp) {
		d.comp[d.index] = 1
		d.index++
	}
	month, day = none, none
	if d.namedMonth == none {
		if d.iso || !isDay(d.comp[0]) {
			year, month, day = d.comp[0], d.comp[1], d.comp[2]
		} else {
			month, day, year = d.comp[0], d.comp[1], d.comp[2]
		}
	} else {
		month = d.namedMonth
		if !isDay(d.comp[0]) {
			year, day = d.comp[0], d.comp[1]
		} else {
			day, year = d.comp[0], d.comp[1]
		}
	}
	if !d.iso {
		if year >= 0 && year <= 49 {
			year += 2000
		} else if year >= 50 && year <= 99 {
			year += 1900
		}
	}
	if month < 1 || month > 12 || !isDay(day) {
		return 0, 0, 0, false
	}
	return year, month - 1, day, true
}

type timeComposer struct {
	comp       [4]int
	index      int
	hourOffset int
}

func (t *timeComposer) add(n int) bool {
	if t.index == len(t.comp) {
		return false
	}
	t.comp[t.index] = n
	t.index++
	return true
}

func (t *timeComposer) addFinal(n int) bool {
	if !t.add(n) {
		return false
	}
	for t.index < len(t.comp) {
		t.comp[t.index] = 0
		t.index++
	}
	return true
}

func (t *timeComposer) isExpecting(n int) bool {
	return t.index == 1 && n >= 0 && n <= 59 || t.index == 2 && n >= 0 && n <= 59 || t.index == 3 && n >= 0 && n <= 999
}

func (t *timeComposer) write() (hour, minute, second, ms int, ok bool) {
	for t.index < len(t.comp) {
		t.comp[t.index] = 0
		t.index++
	}
	hour, minute, second, ms = t.comp[0], t.comp[1], t.comp[2], t.comp[3]
	if t.hourOffset != none {
		if hour < 0 || hour > 12 {
			return 0, 0, 0, 0, false
		}
		hour = hour%12 + t.hourOffset
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 || second < 0 || second > 59 || ms < 0 || ms > 999 {
		// Timme 24 går bara om resten är noll.
		if hour != 24 || minute != 0 || second != 0 || ms != 0 {
			return 0, 0, 0, 0, false
		}
	}
	return hour, minute, second, ms, true
}

type tzComposer struct{ sign, hour, minute int }

func (z *tzComposer) set(hours int) {
	z.sign = 1
	if hours < 0 {
		z.sign = -1
	}
	z.hour = hours * z.sign
	z.minute = 0
}

func (z *tzComposer) setSign(sign int) {
	z.sign = 1
	if sign < 0 {
		z.sign = -1
	}
}

func (z *tzComposer) isExpecting(n int) bool {
	return z.hour != none && z.minute == none && n >= 0 && n <= 59
}

func (z *tzComposer) isUTC() bool { return z.hour == 0 && z.minute == 0 }

// write ger förskjutningen i sekunder, eller local = true om ingen tidszon angavs.
func (z *tzComposer) write() (offset int, local, ok bool) {
	if z.sign == none {
		return 0, true, true
	}
	if z.hour == none {
		z.hour = 0
	}
	if z.minute == none {
		z.minute = 0
	}
	total := int64(z.hour)*3600 + int64(z.minute)*60
	if total > math.MaxInt32>>1 {
		return 0, false, false
	}
	if z.sign < 0 {
		total = -total
	}
	return int(total), false, true
}

type tokenKind int

const (
	tokInvalid tokenKind = iota
	tokUnknown
	tokNumber
	tokSymbol
	tokSpace
	tokKeyword
	tokEnd
)

type keywordKind int

const (
	kwInvalid keywordKind = iota
	kwMonth
	kwTimeSep
	kwAmPm
	kwZone
)

type dateToken struct {
	kind    tokenKind
	keyword keywordKind
	value   int // talet, tecknet (för symboler) eller nyckelordets värde
	length  int
}

func (t dateToken) isSymbol(c rune) bool { return t.kind == tokSymbol && rune(t.value) == c }
func (t dateToken) isSign() bool         { return t.kind == tokSymbol && (t.value == '+' || t.value == '-') }

// sign är 1 för + och -1 för -.
func (t dateToken) sign() int {
	if t.value == '-' {
		return -1
	}
	return 1
}
func (t dateToken) isFixedNumber(n int) bool {
	return t.kind == tokNumber && t.length == n
}
func (t dateToken) isKeywordZ() bool {
	return t.kind == tokKeyword && t.keyword == kwZone && t.length == 1 && t.value == 0
}

var keywords = []struct {
	prefix string
	kind   keywordKind
	value  int
}{
	{"jan", kwMonth, 1}, {"feb", kwMonth, 2}, {"mar", kwMonth, 3}, {"apr", kwMonth, 4},
	{"may", kwMonth, 5}, {"jun", kwMonth, 6}, {"jul", kwMonth, 7}, {"aug", kwMonth, 8},
	{"sep", kwMonth, 9}, {"oct", kwMonth, 10}, {"nov", kwMonth, 11}, {"dec", kwMonth, 12},
	{"am", kwAmPm, 0}, {"pm", kwAmPm, 12},
	{"ut", kwZone, 0}, {"utc", kwZone, 0}, {"z", kwZone, 0}, {"gmt", kwZone, 0},
	{"cdt", kwZone, -5}, {"cst", kwZone, -6}, {"edt", kwZone, -4}, {"est", kwZone, -5},
	{"mdt", kwZone, -6}, {"mst", kwZone, -7}, {"pdt", kwZone, -7}, {"pst", kwZone, -8},
	{"t", kwTimeSep, 0},
}

type dateScanner struct {
	in   []uint16
	pos  int
	next dateToken
}

func (s *dateScanner) Peek() dateToken { return s.next }

func (s *dateScanner) Next() dateToken {
	t := s.next
	s.next = s.scan()
	return t
}

func (s *dateScanner) SkipSymbol(c rune) bool {
	if s.next.isSymbol(c) {
		s.Next()
		return true
	}
	return false
}

func (s *dateScanner) ch() uint16 {
	if s.pos < len(s.in) {
		return s.in[s.pos]
	}
	return 0
}

func isSpace16(c uint16) bool { return IsSpace(rune(c)) }

func (s *dateScanner) scan() dateToken {
	start := s.pos
	if s.pos >= len(s.in) {
		return dateToken{kind: tokEnd}
	}
	c := s.ch()
	switch {
	case c >= '0' && c <= '9':
		// Högst nio siffror räknas; inledande nollor räknas inte men syns i längden.
		n, digits := 0, 0
		for s.ch() == '0' && s.pos < len(s.in) {
			s.pos++
		}
		for s.pos < len(s.in) && s.ch() >= '0' && s.ch() <= '9' {
			if digits < 9 {
				n = n*10 + int(s.ch()-'0')
			}
			digits++
			s.pos++
		}
		return dateToken{kind: tokNumber, value: n, length: s.pos - start}
	case c == ':' || c == '.' || c == ')':
		s.pos++
		return dateToken{kind: tokSymbol, value: int(c), length: 1}
	case c == '-' || c == '+':
		s.pos++
		return dateToken{kind: tokSymbol, value: int(c), length: 1}
	case c >= 'A' && !isSpace16(c):
		var prefix [3]uint16
		length := 0
		for s.pos < len(s.in) && s.ch() >= 'A' && !isSpace16(s.ch()) {
			if length < 3 {
				prefix[length] = s.ch() | 0x20
			}
			length++
			s.pos++
		}
		for _, k := range keywords {
			j := 0
			for j < 3 && (j < len(k.prefix) && prefix[j] == uint16(k.prefix[j]) || j >= len(k.prefix) && prefix[j] == 0) {
				j++
			}
			if j == 3 && (length <= 3 || k.kind == kwMonth) {
				return dateToken{kind: tokKeyword, keyword: k.kind, value: k.value, length: length}
			}
		}
		return dateToken{kind: tokKeyword, keyword: kwInvalid, length: length}
	case isSpace16(c):
		for s.pos < len(s.in) && isSpace16(s.ch()) {
			s.pos++
		}
		return dateToken{kind: tokSpace, length: s.pos - start}
	case c == '(':
		// Parenteser hoppas över, med nästling.
		balance := 0
		for {
			switch s.ch() {
			case ')':
				balance--
			case '(':
				balance++
			}
			s.pos++
			if balance <= 0 || s.pos >= len(s.in) {
				break
			}
		}
		return dateToken{kind: tokUnknown}
	}
	s.pos++
	return dateToken{kind: tokUnknown}
}
