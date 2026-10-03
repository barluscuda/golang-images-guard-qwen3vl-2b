package httpapi

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

func TestWebPValidation(t *testing.T) {
	for _, name := range []string{"landscape.webp", "portrait.webp"} {
		data, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		image, err := ValidateWebP(data, 1920, 1080)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if max(image.Width, image.Height) != 1920 || min(image.Width, image.Height) != 1080 {
			t.Fatal("wrong dimensions")
		}
	}
	data, err := os.ReadFile("testdata/too-tall.webp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateWebP(data, 1920, 1080); !errors.Is(err, ErrDimensions) {
		t.Fatalf("square larger than FHD short side: %v", err)
	}
	if _, err := ValidateWebP([]byte("not WebP"), 1920, 1080); !errors.Is(err, ErrNotWebP) {
		t.Fatal(err)
	}
	data, err = os.ReadFile("testdata/landscape.webp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateWebP(data[:len(data)-1], 1920, 1080); !errors.Is(err, ErrCorruptImage) {
		t.Fatal("accepted truncated image")
	}
	animated := append(append([]byte(nil), data...), []byte("ANIM\x00\x00\x00\x00")...)
	binary.LittleEndian.PutUint32(animated[4:8], uint32(len(animated)-8))
	if _, err := ValidateWebP(animated, 1920, 1080); !errors.Is(err, ErrCorruptImage) {
		t.Fatal("accepted animation")
	}
	// A plausible container/header cannot pass without a decodable image body.
	broken := append([]byte(nil), data...)
	for i := 25; i < len(broken); i++ {
		broken[i] = 0xff
	}
	if _, err := ValidateWebP(broken, 1920, 1080); err == nil {
		t.Fatal("accepted corrupt pixels")
	}
}
