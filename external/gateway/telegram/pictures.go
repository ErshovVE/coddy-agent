//go:build gateway || gateway.telegram

package telegram

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/EvilFreelancer/coddy-agent/internal/session"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// pictureSession is the session a chat turn runs in, as far as sending its
// pictures goes: which session it is, and where its bundle keeps the copies.
type pictureSession interface {
	GetID() string
	GetPersistedSessionDir() string
}

// sendPictures posts the pictures a finished call of the chat's own session
// showed the model (read on an image file), one photo each, captioned with the
// file's name, so the chat sees what the agent looked at while the turn runs.
// The photo is the copy kept with the session, the picture the model was
// shown even when the workspace file changed since. A picture Telegram
// refuses as a photo (a side past its limits) goes as a document; one that
// cannot be read is left out, and the answer still comes. A subagent's calls
// belong to another session and are not the chat's to see.
func (s *Sender) sendPictures(sessionID string, images []session.ToolImage) {
	if len(images) == 0 || s.pictures == nil || sessionID != s.pictures.GetID() {
		return
	}
	dir := strings.TrimSpace(s.pictures.GetPersistedSessionDir())
	if dir == "" {
		return
	}
	assets := session.AssetsPath(dir)
	posted := false
	defer func() {
		if posted {
			s.moveLiveBelow()
		}
	}()
	for _, img := range images {
		// The asset is a bare name inside the session's assets directory; any
		// other spelling is not a copy this session made.
		if img.Asset == "" || filepath.Base(img.Asset) != img.Asset || img.Asset == "." || img.Asset == ".." {
			continue
		}
		path := filepath.Join(assets, img.Asset)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			s.log.Warn("telegram: read picture", "asset", img.Asset, "err", err)
			continue
		}
		name := strings.TrimSpace(img.Name)
		if name == "" {
			name = img.Asset
		}
		file := tgbotapi.FileBytes{Name: name, Bytes: data}
		photo := tgbotapi.NewPhoto(s.chatID, file)
		photo.Caption = name
		if _, err = s.bot.Send(photo); err == nil {
			posted = true
			continue
		}
		// Only Telegram refusing the photo itself (its size, its shape) is
		// worth another try as a document; a rate limit or a failure on the way
		// would only double the traffic. The library leaves the code of an
		// upload's error at zero, so the Bot API's own "Bad Request" wording
		// tells a refusal apart.
		var apiErr *tgbotapi.Error
		if !errors.As(err, &apiErr) || (apiErr.Code != http.StatusBadRequest && !strings.HasPrefix(apiErr.Message, "Bad Request")) {
			s.log.Warn("telegram: send picture", "name", name, "err", err)
			continue
		}
		s.log.Debug("telegram: picture refused as a photo, sending it as a document", "name", name, "err", err)
		doc := tgbotapi.NewDocument(s.chatID, file)
		doc.Caption = name
		if _, err := s.bot.Send(doc); err != nil {
			s.log.Warn("telegram: send picture", "name", name, "err", err)
			continue
		}
		posted = true
	}
}

// moveLiveBelow keeps the answer under the pictures it follows. Without Rich
// Messages the answer grows in one live message sent before the call ran, and
// its final edit would leave the finished answer above the photos. So the
// live message is dropped once a picture is posted: the text streamed so far
// is still in the answer, which comes as a new message below, again a reply
// to the person's message. The old message is deleted when Telegram lets it
// be; when it does not (too old, already gone) the answer still moves below.
// Rich Messages need nothing: a draft is not a message, and the answer is
// sent at the end.
func (s *Sender) moveLiveBelow() {
	if s.rich.enabled {
		return
	}
	s.mu.Lock()
	liveID := s.liveID
	if liveID != 0 {
		s.liveID = 0
		s.replyTo = s.askedID
	}
	s.mu.Unlock()
	if liveID == 0 {
		return
	}
	if _, err := s.bot.Request(tgbotapi.NewDeleteMessage(s.chatID, liveID)); err != nil {
		s.log.Debug("telegram: delete the live message above a picture", "err", err)
	}
}
