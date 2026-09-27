package fs

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EvilFreelancer/coddy-agent/internal/tooling"
)

type attachedImage struct {
	name, mimeType string
	data           []byte
}

// imageEnv is a tool environment whose agent takes every picture it is handed.
func imageEnv(t *testing.T) (*tooling.Env, *[]attachedImage) {
	t.Helper()
	var got []attachedImage
	env := &tooling.Env{CWD: t.TempDir()}
	env.AttachImage = func(name, mimeType string, data []byte) error {
		got = append(got, attachedImage{name, mimeType, data})
		return nil
	}
	return env, &got
}

func solidImage(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	return img
}

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, solidImage(w, h)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeFile(t *testing.T, env *tooling.Env, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(env.CWD, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func runRead(env *tooling.Env, args string) (string, error) {
	return executeRead(context.Background(), args, env)
}

func TestReadHandsTheModelAPictureTellingTheTypeByContent(t *testing.T) {
	env, got := imageEnv(t)
	pngData := encodePNG(t, 40, 30)
	writeFile(t, env, "shot.png", pngData)
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, solidImage(8, 6), nil); err != nil {
		t.Fatal(err)
	}
	// A JPEG saved under a .png name, and one with no extension at all.
	writeFile(t, env, "photo.png", jpg.Bytes())
	writeFile(t, env, "noext", pngData)

	out, err := runRead(env, `{"path":"shot.png"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"shot.png", "PNG image", "40x30"} {
		if !strings.Contains(out, want) {
			t.Errorf("result %q does not say %q", out, want)
		}
	}
	if _, err := runRead(env, `{"path":"photo.png"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := runRead(env, `{"path":"`+filepath.Join(env.CWD, "noext")+`","offset":3,"limit":1}`); err != nil {
		t.Fatalf("an image read with a line range: %v", err)
	}

	if len(*got) != 3 {
		t.Fatalf("attached %d pictures, want 3", len(*got))
	}
	want := []attachedImage{{"shot.png", "image/png", pngData}, {"photo.png", "image/jpeg", jpg.Bytes()}, {"noext", "image/png", pngData}}
	for i, w := range want {
		g := (*got)[i]
		if g.name != w.name || g.mimeType != w.mimeType || !bytes.Equal(g.data, w.data) {
			t.Errorf("picture %d = %s %s (%d bytes), want %s %s (%d bytes)", i, g.name, g.mimeType, len(g.data), w.name, w.mimeType, len(w.data))
		}
	}
}

func TestReadOfTextNamedLikeAnImageStaysText(t *testing.T) {
	env, got := imageEnv(t)
	writeFile(t, env, "fake.png", []byte("just text\n"))
	out, err := runRead(env, `{"path":"fake.png"}`)
	if err != nil || out != "just text\n" {
		t.Fatalf("read = %q, %v", out, err)
	}
	if len(*got) != 0 {
		t.Error("text was attached as a picture")
	}
}

func TestReadOfAnImageWithoutAnAgentIsRefusedAsBinary(t *testing.T) {
	env := &tooling.Env{CWD: t.TempDir()}
	writeFile(t, env, "shot.png", encodePNG(t, 2, 2))
	_, err := runRead(env, `{"path":"shot.png"}`)
	if err == nil || !strings.Contains(err.Error(), "binary") || !strings.Contains(err.Error(), "image/png") {
		t.Fatalf("err = %v, want the binary refusal naming image/png", err)
	}
}

func TestReadOfAnImageCarriesTheAgentsRefusal(t *testing.T) {
	env := &tooling.Env{CWD: t.TempDir()}
	env.AttachImage = func(string, string, []byte) error {
		return errors.New("the session's model m does not read images")
	}
	writeFile(t, env, "shot.png", encodePNG(t, 2, 2))
	_, err := runRead(env, `{"path":"shot.png"}`)
	if err == nil || !strings.Contains(err.Error(), "does not read images") || !strings.Contains(err.Error(), "shot.png") {
		t.Fatalf("err = %v, want the agent's refusal naming the file", err)
	}
}

func TestReadRefusesAPictureNoProviderWouldTake(t *testing.T) {
	env, got := imageEnv(t)

	huge := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, readImageMaxBytes)...)
	writeFile(t, env, "huge.png", huge)
	if _, err := runRead(env, `{"path":"huge.png"}`); err == nil || !strings.Contains(err.Error(), "MB") {
		t.Errorf("an oversized picture: err = %v, want the size limit named", err)
	}

	writeFile(t, env, "wide.png", encodePNG(t, readImageMaxSide+1, 1))
	if _, err := runRead(env, `{"path":"wide.png"}`); err == nil || !strings.Contains(err.Error(), "8001x1") {
		t.Errorf("a picture over the side limit: err = %v, want its size named", err)
	}

	writeFile(t, env, "broken.png", []byte("\x89PNG\r\n\x1a\nnot really"))
	if _, err := runRead(env, `{"path":"broken.png"}`); err == nil || !strings.Contains(err.Error(), "cannot be decoded") {
		t.Errorf("a broken PNG: err = %v, want it refused as undecodable", err)
	}

	if len(*got) != 0 {
		t.Errorf("a refused picture was attached: %d", len(*got))
	}
}

func TestReadShowsTheFirstFrameOfAnAnimatedGIF(t *testing.T) {
	env, got := imageEnv(t)
	frame := func(c uint8) *image.Paletted {
		p := image.NewPaletted(image.Rect(0, 0, 6, 4), palette.Plan9)
		for i := range p.Pix {
			p.Pix[i] = c
		}
		return p
	}
	var anim bytes.Buffer
	if err := gif.EncodeAll(&anim, &gif.GIF{Image: []*image.Paletted{frame(10), frame(200)}, Delay: []int{5, 5}}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, env, "anim.gif", anim.Bytes())
	var still bytes.Buffer
	if err := gif.Encode(&still, frame(10), nil); err != nil {
		t.Fatal(err)
	}
	writeFile(t, env, "still.gif", still.Bytes())

	out, err := runRead(env, `{"path":"anim.gif"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "first frame") || !strings.Contains(out, "6x4") {
		t.Errorf("result %q does not say the model sees the first frame", out)
	}
	if _, err := runRead(env, `{"path":"still.gif"}`); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 2 {
		t.Fatalf("attached %d pictures, want 2", len(*got))
	}
	for i, g := range *got {
		if g.mimeType != "image/png" {
			t.Fatalf("GIF %d went as %s, want the PNG of its first frame", i, g.mimeType)
		}
		img, err := png.Decode(bytes.NewReader(g.data))
		if err != nil || img.Bounds().Dx() != 6 || img.Bounds().Dy() != 4 {
			t.Fatalf("GIF %d decodes to %v (%v), want a 6x4 frame", i, img, err)
		}
	}
}

// A GIF is read only as far as its first frame: the frames after it are never
// decoded, so a small file of many large frames cannot fill the memory. The
// proof is a GIF whose second frame is cut short, which decoding every frame
// would refuse.
func TestReadDecodesOnlyTheFirstFrameOfAGIF(t *testing.T) {
	env, got := imageEnv(t)
	frame := func(c uint8) *image.Paletted {
		p := image.NewPaletted(image.Rect(0, 0, 6, 4), palette.Plan9)
		for i := range p.Pix {
			p.Pix[i] = c
		}
		return p
	}
	var one bytes.Buffer
	if err := gif.EncodeAll(&one, &gif.GIF{Image: []*image.Paletted{frame(10)}, Delay: []int{5}}); err != nil {
		t.Fatal(err)
	}
	// The one-frame file without its trailer, then the image descriptor of a
	// second 6x4 frame and its LZW code size, and the file ends there.
	cut := append([]byte(nil), one.Bytes()[:one.Len()-1]...)
	cut = append(cut, 0x2C, 0, 0, 0, 0, 6, 0, 4, 0, 0x00, 0x08)
	if _, err := gif.DecodeAll(bytes.NewReader(cut)); err == nil {
		t.Fatal("the fixture decodes whole; it must not")
	}
	writeFile(t, env, "cut.gif", cut)
	if _, err := runRead(env, `{"path":"cut.gif"}`); err != nil {
		t.Fatalf("read of a GIF with a broken second frame: %v", err)
	}
	if len(*got) != 1 || (*got)[0].mimeType != "image/png" {
		t.Fatalf("attached %+v, want the first frame as a PNG", *got)
	}
}

// webpVP8X is the smallest WebP header the size reader understands: RIFF,
// WEBP and an extended-format chunk carrying the canvas size.
func webpVP8X(w, h int) []byte {
	chunk := make([]byte, 10)
	put24 := func(b []byte, v int) { b[0], b[1], b[2] = byte(v), byte(v>>8), byte(v>>16) }
	put24(chunk[4:7], w-1)
	put24(chunk[7:10], h-1)
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(4+8+len(chunk)))
	buf.WriteString("WEBPVP8X")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(chunk)))
	buf.Write(chunk)
	return buf.Bytes()
}

func TestReadHandsTheModelAWebPWithItsSize(t *testing.T) {
	env, got := imageEnv(t)
	writeFile(t, env, "pic.webp", webpVP8X(640, 480))
	out, err := runRead(env, `{"path":"pic.webp"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "WebP image") || !strings.Contains(out, "640x480") {
		t.Errorf("result %q does not name the WebP and its size", out)
	}
	if len(*got) != 1 || (*got)[0].mimeType != "image/webp" {
		t.Fatalf("attached %+v, want one image/webp", *got)
	}
	writeFile(t, env, "wide.webp", webpVP8X(9000, 10))
	if _, err := runRead(env, `{"path":"wide.webp"}`); err == nil || !strings.Contains(err.Error(), "9000x10") {
		t.Errorf("a WebP over the side limit: err = %v", err)
	}
}

func TestReadDescriptionTellsTheModelAboutPictures(t *testing.T) {
	desc := ReadTool().Definition.Description
	for _, want := range []string{"PNG", "JPEG", "GIF", "WebP", "picture"} {
		if !strings.Contains(desc, want) {
			t.Errorf("read description does not mention %q", want)
		}
	}
	var names []string
	RegisterBuiltins(func(tool *tooling.Tool) { names = append(names, tool.Definition.Name) })
	for _, n := range names {
		if n == "view_image" {
			t.Error("view_image is still a built-in: read shows pictures")
		}
	}
}
