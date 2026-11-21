// Command mkicon packs the Fileion software mark into the files the program needs. It draws nothing: the images in winres/icons/ are the source.
//
// The mark is the Fileion software logo (blue download arrow over a tray), public/images/software/software-logo.svg in the Fileion site (viewBox 776 x 679.62).
// winres/icons/icon-{16,24,32,48,64,192,256,512}.png were made once from that SVG, with no extra tools and no network:
//
//	a page with <img src="software-logo.svg"> (square box of N x N px, the SVG at width N, centred, transparent background) opened in
//	Chromium at an N x N viewport, screenshot with a transparent background (Playwright page.screenshot({omitBackground: true})), for each N.
//
// Those PNGs are committed, so a build never needs the SVG, a browser or the network. To change the mark, replace the PNGs and run:
//
//	go run ./tools/mkicon winres icons out
//
// which writes winres/icon.ico (16, 24, 32, 48, 64 and 256 px images, PNG-compressed), internal/ui/icon-{32,64,192,512}.png (the progress window's favicon and
// web manifest) and packaging/fileion-installer.png (the 256 px AppImage icon), so the window, the AppImage and the .exe show the same mark.
// Standard library only.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/mkicon run   (run from the repository root)")
		os.Exit(2)
	}
	src := func(n int) []byte {
		b, err := os.ReadFile(filepath.Join("winres", "icons", fmt.Sprintf("icon-%d.png", n)))
		if err != nil {
			fail(err)
		}
		if !bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) {
			fail(fmt.Errorf("icon-%d.png is not a PNG", n))
		}
		return b
	}
	for _, n := range []int{32, 64, 192, 512} {
		write(filepath.Join("internal", "ui", fmt.Sprintf("icon-%d.png", n)), src(n))
	}
	write(filepath.Join("packaging", "fileion-installer.png"), src(256))

	sizes := []int{16, 24, 32, 48, 64, 256}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, []uint16{0, 1, uint16(len(sizes))}) // ICONDIR: reserved, type icon, count
	offset := 6 + 16*len(sizes)
	for _, s := range sizes {
		dim := byte(s)
		if s == 256 {
			dim = 0 // 0 means 256
		}
		b := src(s)
		out.Write([]byte{dim, dim, 0, 0})
		binary.Write(&out, binary.LittleEndian, []uint16{1, 32}) // planes, bits per pixel
		binary.Write(&out, binary.LittleEndian, []uint32{uint32(len(b)), uint32(offset)})
		offset += len(b)
	}
	for _, s := range sizes {
		out.Write(src(s))
	}
	write(filepath.Join("winres", "icon.ico"), out.Bytes())
}

func write(path string, b []byte) {
	if err := os.WriteFile(path, b, 0o644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "mkicon:", err)
	os.Exit(1)
}
