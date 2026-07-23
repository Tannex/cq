package decode

import "testing"

// FuzzDisplayBytes renders arbitrary record bytes through the display
// conversion for both an EBCDIC and an ASCII codepage — the path every raw
// data set byte takes before reaching the screen. Any input must produce a
// string, never a panic.
func FuzzDisplayBytes(f *testing.F) {
	f.Add([]byte("HELLO WORLD"))
	f.Add([]byte{0x00, 0x01, 0x1B, 0x5B, 0x33, 0x31, 0x6D}) // control bytes and an ANSI escape
	f.Add([]byte{0xFF, 0xFE, 0x80, 0xC3, 0x28})             // invalid UTF-8 shapes
	f.Add([]byte{0x40, 0xC8, 0xC5, 0xD3, 0xD3, 0xD6})       // EBCDIC text
	f.Add(make([]byte, 4096))

	codepages := []*Charmap{}
	for _, name := range []string{"cp037", "latin1"} {
		cm, err := Codepage(name)
		if err != nil {
			f.Fatal(err)
		}
		codepages = append(codepages, cm)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		for _, cm := range codepages {
			_ = DisplayBytes(data, cm)
		}
	})
}
