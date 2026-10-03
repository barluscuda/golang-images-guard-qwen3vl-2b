package httpapi

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/core"
	"golang.org/x/image/webp"
)

var (
	ErrNotWebP      = errors.New("not WebP")
	ErrDimensions   = errors.New("invalid dimensions")
	ErrCorruptImage = errors.New("corrupt or animated WebP")
)

func ValidateWebP(data []byte, maxLong, maxShort int) (core.Image, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return core.Image{}, ErrNotWebP
	}
	if uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
		return core.Image{}, ErrCorruptImage
	}
	frames, extended := 0, false
	for pos := 12; pos < len(data); {
		if len(data)-pos < 8 {
			return core.Image{}, ErrCorruptImage
		}
		kind := string(data[pos : pos+4])
		length := uint64(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		end := uint64(pos) + 8 + length
		next := end + length%2
		if next > uint64(len(data)) {
			return core.Image{}, ErrCorruptImage
		}
		switch kind {
		case "ANIM", "ANMF":
			return core.Image{}, ErrCorruptImage
		case "VP8X":
			if extended || pos != 12 || length != 10 {
				return core.Image{}, ErrCorruptImage
			}
			header := data[pos+8 : end]
			if header[0]&0xc3 != 0 || header[1] != 0 || header[2] != 0 || header[3] != 0 {
				return core.Image{}, ErrCorruptImage
			}
			extended = true
		case "VP8 ", "VP8L":
			frames++
		}
		if length%2 != 0 && data[end] != 0 {
			return core.Image{}, ErrCorruptImage
		}
		pos = int(next)
	}
	if frames != 1 {
		return core.Image{}, ErrCorruptImage
	}
	config, err := webp.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return core.Image{}, ErrCorruptImage
	}
	long, short := max(config.Width, config.Height), min(config.Width, config.Height)
	if short <= 0 || long > maxLong || short > maxShort {
		return core.Image{}, ErrDimensions
	}
	decoded, err := webp.Decode(bytes.NewReader(data))
	if err != nil || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return core.Image{}, ErrCorruptImage
	}
	return core.Image{Data: data, Width: config.Width, Height: config.Height}, nil
}
