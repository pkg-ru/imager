package vips

import (
	"testing"
)

// TestHeifSequenceEncodeAV1 проверяет, что sequence encoder (AV1) реально
// пишет анимированный AVIF: 2 кадра 32x32 (красный/зелёный), duration 100
// тиков @1000Hz, и что результат содержит sequence track с 2 кадрами.
func TestHeifSequenceEncodeAV1(t *testing.T) {
	const w, h = 32, 32
	enc, err := NewHeifSequenceEncoder(HeifCompressionAV1, HeifSequenceParams{
		Width:       w,
		Height:      h,
		Timescale:   1000,
		Repetitions: 0, // бесконечно
		Quality:     60,
		Speed:       6,
	})
	if err != nil {
		t.Fatalf("NewHeifSequenceEncoder: %v", err)
	}
	defer enc.Close()

	// Кадр 0: красный, кадр 1: зелёный.
	for f := 0; f < 2; f++ {
		pixels := make([]byte, w*h*3)
		for i := 0; i < w*h; i++ {
			if f == 0 {
				pixels[i*3+0] = 255
			} else {
				pixels[i*3+1] = 255
			}
		}
		if err := enc.AddFrame(pixels, false, 100); err != nil {
			t.Fatalf("AddFrame(%d): %v", f, err)
		}
	}
	if err := enc.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	data, err := enc.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("empty output")
	}
	if !HeifHasSequence(data) {
		t.Fatal("output has no sequence track")
	}
	if n := HeifSequenceFrameCount(data); n != 2 {
		t.Fatalf("frame count = %d, want 2", n)
	}
	if d := HeifSequenceFirstDuration(data); d != 100 {
		t.Fatalf("first duration = %d, want 100", d)
	}
}

// TestHeifSequenceEncodeRGBA проверяет кодирование с альфа-каналом.
func TestHeifSequenceEncodeRGBA(t *testing.T) {
	const w, h = 16, 16
	enc, err := NewHeifSequenceEncoder(HeifCompressionAV1, HeifSequenceParams{
		Width:       w,
		Height:      h,
		Timescale:   1000,
		Repetitions: 0,
		Quality:     60,
		Speed:       6,
	})
	if err != nil {
		t.Fatalf("NewHeifSequenceEncoder: %v", err)
	}
	defer enc.Close()

	pixels := make([]byte, w*h*4)
	for i := 0; i < w*h; i++ {
		pixels[i*4+0] = 255
		pixels[i*4+1] = 0
		pixels[i*4+2] = 0
		pixels[i*4+3] = 128
	}
	if err := enc.AddFrame(pixels, true, 50); err != nil {
		t.Fatalf("AddFrame: %v", err)
	}
	if err := enc.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	data, err := enc.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if !HeifHasSequence(data) {
		t.Fatal("output has no sequence track")
	}
	if n := HeifSequenceFrameCount(data); n != 1 {
		t.Fatalf("frame count = %d, want 1", n)
	}
}
