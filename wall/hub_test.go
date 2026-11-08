package wall

import (
	"bufio"
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func testJPEG(t *testing.T, seed uint8, q int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(x*4) + seed, uint8(y*5) ^ seed, seed * 7, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: q}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestSplitJPEG(t *testing.T) {
	var frames [][]byte
	var stream bytes.Buffer
	stream.WriteString("garbage before the first frame\xff\x00")
	for i := 0; i < 20; i++ {
		// quality 1 gives quantisation tables full of 0xFF bytes, which a
		// naive search for the end marker would trip over
		f := testJPEG(t, uint8(i*13), []int{1, 50, 100}[i%3])
		frames = append(frames, f)
		stream.Write(f)
	}
	var got [][]byte
	err := splitJPEG(bufio.NewReader(&stream), func(b []byte) { got = append(got, append([]byte(nil), b...)) })
	if err == nil {
		t.Fatal("want EOF at the end of the stream")
	}
	if len(got) != len(frames) {
		t.Fatalf("got %d frames, want %d", len(got), len(frames))
	}
	for i := range frames {
		if !bytes.Equal(got[i], frames[i]) {
			t.Errorf("frame %d differs (%d vs %d bytes)", i, len(got[i]), len(frames[i]))
		}
		if _, err := jpeg.Decode(bytes.NewReader(got[i])); err != nil {
			t.Errorf("frame %d does not decode: %v", i, err)
		}
	}
}
