package main

import (
	"fmt"
	"io"
	"strings"

	"rsc.io/qr"
)

// printQR renders text as a QR code with Unicode half blocks (two modules per character cell).
// Light modules are drawn as filled blocks, which looks right on the usual dark terminal; invert
// swaps that for light-themed terminals. The app's scanner accepts both polarities anyway.
func printQR(w io.Writer, text string, invert, ascii bool) error {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return err
	}
	const quiet = 2
	n := code.Size + 2*quiet
	dark := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		if x < 0 || y < 0 || x >= code.Size || y >= code.Size {
			return false
		}
		return code.Black(x, y)
	}
	// "on" = drawn as ink on screen.
	on := func(x, y int) bool {
		if y >= n {
			return false
		}
		if invert {
			return dark(x, y)
		}
		return !dark(x, y)
	}
	var b strings.Builder
	if ascii {
		for y := range n {
			for x := range n {
				if on(x, y) {
					b.WriteString("##")
				} else {
					b.WriteString("  ")
				}
			}
			b.WriteByte('\n')
		}
	} else {
		for y := 0; y < n; y += 2 {
			for x := range n {
				top, bot := on(x, y), on(x, y+1)
				switch {
				case top && bot:
					b.WriteRune('█')
				case top:
					b.WriteRune('▀')
				case bot:
					b.WriteRune('▄')
				default:
					b.WriteByte(' ')
				}
			}
			b.WriteByte('\n')
		}
	}
	_, err = fmt.Fprint(w, b.String())
	return err
}
