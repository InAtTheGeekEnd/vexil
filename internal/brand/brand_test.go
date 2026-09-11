package brand

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAccent(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"#4F46E5", "#4F46E5", false},
		{"4f46e5", "#4F46E5", false},
		{" #abc ", "#AABBCC", false},
		{"abc", "#AABBCC", false},
		{"#GGGGGG", "", true},
		{"#12345", "", true},
		{"", "", true},
		{"red", "", true},
	}
	for _, tc := range tests {
		got, err := ParseAccent(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ParseAccent(%q) = %q, %v; want %q, err=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestTextOnPassesAA(t *testing.T) {
	tests := []struct{ bg, want string }{
		{"#FFFFFF", "#000000"},
		{"#000000", "#FFFFFF"},
		{"#FACC15", "#000000"}, // yellow
		{"#1D4ED8", "#FFFFFF"}, // blue
		{"#4F46E5", "#FFFFFF"},
	}
	for _, tc := range tests {
		got := TextOn(tc.bg)
		if got != tc.want {
			t.Errorf("TextOn(%s) = %s, want %s", tc.bg, got, tc.want)
		}
		if c := Contrast(tc.bg, got); c < 4.5 {
			t.Errorf("TextOn(%s) contrast %.2f is below AA", tc.bg, c)
		}
	}
}

func TestParseLogo(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 20)...)
	svg := []byte(`<?xml version="1.0"?><!-- c --><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`)
	tests := []struct {
		name string
		data []byte
		typ  string
		err  error
	}{
		{"png", png, "image/png", nil},
		{"svg", svg, "image/svg+xml", nil},
		{"html", []byte("<html><body>hi</body></html>"), "", ErrLogoType},
		{"text", []byte("hello"), "", ErrLogoType},
		{"empty", nil, "", ErrLogoType},
		{"large", []byte(strings.Repeat("<svg>", MaxLogoSize/5+1)), "", ErrLogoTooLarge},
	}
	for _, tc := range tests {
		l, err := ParseLogo(tc.data)
		if !errors.Is(err, tc.err) || l.Type != tc.typ {
			t.Errorf("%s: type %q err %v; want %q %v", tc.name, l.Type, err, tc.typ, tc.err)
		}
		if err == nil && l.Version() == "" {
			t.Errorf("%s: empty version", tc.name)
		}
	}
}
