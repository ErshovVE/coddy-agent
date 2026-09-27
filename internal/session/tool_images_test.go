package session

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pngBytes(t *testing.T, w, h int, fill color.Color) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSaveToolImageAssetKeepsThePictureUnderAContentName(t *testing.T) {
	dir := t.TempDir()
	red := pngBytes(t, 400, 300, color.NRGBA{R: 255, A: 255})

	asset, thumb, err := SaveToolImageAsset(dir, "shot.png", "image/png", red)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(asset) != AssetsPath(dir) {
		t.Fatalf("asset %s is not in the session's assets directory", asset)
	}
	name := filepath.Base(asset)
	if !strings.HasPrefix(name, "shot-") || !strings.HasSuffix(name, ".png") {
		t.Errorf("asset name %q, want shot-<digest>.png", name)
	}
	if got, _ := os.ReadFile(asset); !bytes.Equal(got, red) {
		t.Error("the asset does not hold the picture")
	}
	if info, err := os.Stat(asset); err != nil || info.Mode().Perm()&0o222 != 0 {
		t.Errorf("asset mode = %v, want read-only", info.Mode())
	}
	if thumb != AssetThumbnailPath(dir, name) {
		t.Errorf("thumbnail = %q, want %q", thumb, AssetThumbnailPath(dir, name))
	}
	cfg, _, err := image.DecodeConfig(bytesReader(t, thumb))
	if err != nil || cfg.Width > assetThumbnailMaxEdge || cfg.Height > assetThumbnailMaxEdge {
		t.Errorf("thumbnail %dx%d (%v), want bounded to %d", cfg.Width, cfg.Height, err, assetThumbnailMaxEdge)
	}

	again, _, err := SaveToolImageAsset(dir, "shot.png", "image/png", red)
	if err != nil || again != asset {
		t.Errorf("the same picture read again is stored as %q (%v), want the first copy %q", again, err, asset)
	}
	green := pngBytes(t, 4, 3, color.NRGBA{G: 255, A: 255})
	other, _, err := SaveToolImageAsset(dir, "shot.png", "image/png", green)
	if err != nil || other == asset {
		t.Errorf("a changed picture under the same name is stored as %q (%v), want a copy of its own", other, err)
	}
	if got, _ := os.ReadFile(asset); !bytes.Equal(got, red) {
		t.Error("the earlier copy changed when the file was read again")
	}
}

func bytesReader(t *testing.T, path string) *bytes.Reader {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(data)
}

func TestSaveToolImageAssetNamesTheCopyByItsType(t *testing.T) {
	dir := t.TempDir()
	// An animated GIF reaches the model as the PNG of its first frame.
	asset, _, err := SaveToolImageAsset(dir, "anim.gif", "image/png", pngBytes(t, 2, 2, color.Black))
	if err != nil {
		t.Fatal(err)
	}
	if name := filepath.Base(asset); !strings.HasPrefix(name, "anim-") || filepath.Ext(name) != ".png" {
		t.Errorf("asset name %q, want anim-<digest>.png", name)
	}
	// A name that tries to leave the directory stays a base name.
	asset, _, err = SaveToolImageAsset(dir, "../../etc/x.png", "image/png", pngBytes(t, 2, 2, color.White))
	if err != nil || filepath.Dir(asset) != AssetsPath(dir) {
		t.Errorf("asset %q (%v) left the assets directory", asset, err)
	}
}

func TestSaveToolImageAssetWithoutASessionDirectorySavesNothing(t *testing.T) {
	asset, thumb, err := SaveToolImageAsset("", "shot.png", "image/png", []byte("x"))
	if asset != "" || thumb != "" || err != nil {
		t.Errorf("got %q %q %v, want nothing saved", asset, thumb, err)
	}
}

func TestAssetRoutesEscapeTheNames(t *testing.T) {
	if got := AssetRoute("sess_1", "a b#.png"); got != "/coddy/sessions/sess_1/assets/a%20b%23.png" {
		t.Errorf("AssetRoute = %q", got)
	}
	if got := AssetThumbnailRoute("sess 1", "a.png"); got != "/coddy/sessions/sess%201/assets/a.png/thumbnail" {
		t.Errorf("AssetThumbnailRoute = %q", got)
	}
}

func TestToolImagesFromMetaReadsTheValueAndItsJSON(t *testing.T) {
	images := []ToolImage{{Name: "shot.png", MIMEType: "image/png", Asset: "shot-1.png", URL: "/u", PreviewURL: "/p"}}
	meta := ToolImagesMeta(nil, images)

	if got := ToolImagesFromMeta(meta); len(got) != 1 || got[0] != images[0] {
		t.Errorf("in process: %v, want %v", got, images)
	}

	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"images":[{"name":"shot.png","mime_type":"image/png","asset":"shot-1.png","url":"/u","preview_url":"/p"}]`) {
		t.Errorf("wire form %s", raw)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := ToolImagesFromMeta(decoded); len(got) != 1 || got[0] != images[0] {
		t.Errorf("after JSON: %v, want %v", got, images)
	}

	if got := ToolImagesFromMeta(nil); got != nil {
		t.Errorf("no meta: %v", got)
	}
	if got := ToolImagesFromMeta(map[string]interface{}{"coddy": map[string]interface{}{"todoPlan": 1}}); got != nil {
		t.Errorf("no images: %v", got)
	}
}

func TestToolImagesMetaKeepsWhatTheMetaAlreadyHolds(t *testing.T) {
	meta := map[string]interface{}{"coddy": map[string]interface{}{"todoPlan": "kept"}}
	meta = ToolImagesMeta(meta, []ToolImage{{Name: "a.png"}})
	coddy := meta["coddy"].(map[string]interface{})
	if coddy["todoPlan"] != "kept" || coddy["images"] == nil {
		t.Errorf("meta = %v", meta)
	}
	if got := ToolImagesMeta(nil, nil); got != nil {
		t.Errorf("no images still made meta %v", got)
	}
}
