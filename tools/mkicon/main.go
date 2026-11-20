// Command mkicon draws the Fileion mark (a purple rounded square with a white N: the first three paths of
// public/assets/images/logo/logo.svg in the Fileion site) and writes winres/icon.ico with 16, 24, 32, 48, 64 and 256 px images.
// With a second argument (a folder) it also writes icon-32.png, icon-64.png, icon-192.png, icon-256.png and icon-512.png for the progress window's favicon and
// web manifest and the AppImage icon (icon-256.png becomes packaging/fileion-installer.png), so the window, the AppImage and the .exe show the same mark.
// Standard library only, so the icon is reproducible from this file: go run ./tools/mkicon winres/icon.ico internal/ui
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

// The mark in the SVG's 54 x 54 box.
var (
	purple = color.NRGBA{0x74, 0x44, 0xFD, 0xFF}
	nLeft  = [][2]float64{{15, 16.001}, {24.401, 16.001}, {30.882, 22.484}, {30.882, 29.465}, {20.307, 18.946}, {20.274, 38.307}, {15, 38.307}}
	nRight = [][2]float64{{38.631, 38.307}, {29.232, 38.307}, {22.752, 31.823}, {22.752, 24.839}, {33.327, 35.356}, {33.359, 16}, {38.631, 16}}
)

func inRoundRect(x, y float64) bool {
	const r = 16.0
	if x < 0 || y < 0 || x > 54 || y > 54 {
		return false
	}
	cx, cy := math.Min(math.Max(x, r), 54-r), math.Min(math.Max(y, r), 54-r)
	return math.Hypot(x-cx, y-cy) <= r
}

func inPoly(p [][2]float64, x, y float64) bool {
	in := false
	for i, j := 0, len(p)-1; i < len(p); j, i = i, i+1 {
		if (p[i][1] > y) != (p[j][1] > y) && x < (p[j][0]-p[i][0])*(y-p[i][1])/(p[j][1]-p[i][1])+p[i][0] {
			in = !in
		}
	}
	return in
}

func render(size int) *image.NRGBA {
	const ss = 8 // samples per pixel side
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var a, w int
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x, y := (float64(px)+(float64(sx)+0.5)/ss)*54/float64(size), (float64(py)+(float64(sy)+0.5)/ss)*54/float64(size)
					if inRoundRect(x, y) {
						a++
						if inPoly(nLeft, x, y) || inPoly(nRight, x, y) {
							w++
						}
					}
				}
			}
			if a == 0 {
				continue
			}
			// white over purple; alpha = how much of the pixel the square covers
			mix := func(p, white uint8) uint8 { return uint8((int(p)*(a-w) + int(white)*w + a/2) / a) }
			img.SetNRGBA(px, py, color.NRGBA{mix(purple.R, 255), mix(purple.G, 255), mix(purple.B, 255), uint8((a*255 + ss*ss/2) / (ss * ss))})
		}
	}
	return img
}

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		os.Stderr.WriteString("usage: mkicon out.ico [png-folder]\n")
		os.Exit(2)
	}
	if len(os.Args) == 3 {
		for _, s := range []int{32, 64, 192, 256, 512} {
			var b bytes.Buffer
			enc := png.Encoder{CompressionLevel: png.BestCompression}
			if err := enc.Encode(&b, render(s)); err != nil {
				panic(err)
			}
			if err := os.WriteFile(filepath.Join(os.Args[2], "icon-"+strconv.Itoa(s)+".png"), b.Bytes(), 0o644); err != nil {
				panic(err)
			}
		}
	}
	sizes := []int{16, 24, 32, 48, 64, 256}
	var pngs [][]byte
	for _, s := range sizes {
		var b bytes.Buffer
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&b, render(s)); err != nil {
			panic(err)
		}
		pngs = append(pngs, b.Bytes())
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, []uint16{0, 1, uint16(len(sizes))}) // ICONDIR: reserved, type icon, count
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		dim := byte(s)
		if s == 256 {
			dim = 0 // 0 means 256
		}
		out.Write([]byte{dim, dim, 0, 0})
		binary.Write(&out, binary.LittleEndian, []uint16{1, 32}) // planes, bits per pixel
		binary.Write(&out, binary.LittleEndian, []uint32{uint32(len(pngs[i])), uint32(offset)})
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		out.Write(p)
	}
	if err := os.WriteFile(os.Args[1], out.Bytes(), 0o644); err != nil {
		panic(err)
	}
}
