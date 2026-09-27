package session

import (
	"testing"

	"github.com/EvilFreelancer/coddy-agent/internal/llm"
)

func TestToolImagePartsAddUpAndStayApartFromAttachments(t *testing.T) {
	st := &State{}
	st.SetPendingImageParts([]llm.ImagePart{{Name: "attached.png"}})

	st.AppendToolImageParts([]llm.ImagePart{{Name: "first.png"}})
	st.AppendToolImageParts([]llm.ImagePart{{Name: "second.png"}})

	got := st.TakeToolImageParts()
	if len(got) != 2 || got[0].Name != "first.png" || got[1].Name != "second.png" {
		t.Fatalf("took %v, want first.png then second.png", got)
	}
	if rest := st.TakeToolImageParts(); len(rest) != 0 {
		t.Errorf("take left %d tool images queued", len(rest))
	}
	if attached := st.TakePendingImageParts(); len(attached) != 1 || attached[0].Name != "attached.png" {
		t.Errorf("attachments = %v, want only attached.png", attached)
	}
}
