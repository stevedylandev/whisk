package ui

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestUploadCmdPutsFile(t *testing.T) {
	var gotBody, gotCT, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cmd := uploadCmd(srv.URL, "text/plain", []byte("hello"), "file.txt")
	msg := cmd()

	done, ok := msg.(uploadDoneMsg)
	if !ok {
		t.Fatalf("upload cmd returned %T, want uploadDoneMsg", msg)
	}
	if done.err != nil {
		t.Fatalf("upload err = %v", done.err)
	}
	if gotMethod != http.MethodPut {
		t.Fatalf("method = %q, want PUT", gotMethod)
	}
	if gotBody != "hello" {
		t.Fatalf("body = %q, want hello", gotBody)
	}
	if gotCT != "text/plain" {
		t.Fatalf("content-type = %q, want text/plain", gotCT)
	}
	if done.path != "file.txt" {
		t.Fatalf("path = %q", done.path)
	}
}

func TestUploadCmdSurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	cmd := uploadCmd(srv.URL, "", []byte("x"), "f")
	done := cmd().(uploadDoneMsg)
	if done.err == nil {
		t.Fatal("expected an error for non-2xx response")
	}
}

// noisyImage returns an image of random pixels, which compresses poorly.
func noisyImage(w, h int, alpha bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		img.Pix[i] = uint8(r.Uint32())
	}
	if !alpha {
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 0xff
		}
	}
	return img
}

func TestReadUploadFileCompressesLargeImage(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, noisyImage(2000, 2000, false)); err != nil {
		t.Fatal(err)
	}
	if buf.Len() <= maxImageUploadSize {
		t.Fatalf("fixture too small: %d", buf.Len())
	}
	path := filepath.Join(t.TempDir(), "big.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	up, err := readUploadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(up.data) > maxImageUploadSize {
		t.Fatalf("data = %d bytes, want <= %d", len(up.data), maxImageUploadSize)
	}
	if up.mimeType != "image/jpeg" || up.name != "big.jpg" {
		t.Fatalf("got %q %q, want image/jpeg big.jpg", up.mimeType, up.name)
	}
	if _, err := jpeg.Decode(bytes.NewReader(up.data)); err != nil {
		t.Fatalf("result not a jpeg: %v", err)
	}
}

func TestCompressImageKeepsTransparencyAsPNG(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, noisyImage(2000, 2000, true)); err != nil {
		t.Fatal(err)
	}
	out, outType, outName, ok := compressImage(buf.Bytes(), "image/png", "a.png")
	if !ok {
		t.Fatal("compression failed")
	}
	if len(out) > maxImageUploadSize || outType != "image/png" || outName != "a.png" {
		t.Fatalf("got %d bytes %q %q", len(out), outType, outName)
	}
}

func TestReadUploadFileLeavesSmallImage(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, noisyImage(64, 64, false)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "small.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	up, err := readUploadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(up.data, buf.Bytes()) || up.mimeType != "image/png" || up.name != "small.png" {
		t.Fatal("small image was modified")
	}
}
