package embed

import (
	"strconv"

	fastembed "github.com/anush008/fastembed-go"
)

type PromptScheme string

const (
	PromptBGE   PromptScheme = "bge"
	PromptBGEZH PromptScheme = "bge-zh"
	PromptNone  PromptScheme = "none"
)

type FastEmbedProfile struct {
	Name          string
	Enum          fastembed.EmbeddingModel
	Dim           int
	MaxLength     int
	QueryPrefix   string
	PassagePrefix string
	PromptScheme  PromptScheme
	Description   string
}

func (p FastEmbedProfile) Identity() string {
	return "fastembed:" + p.Name + ":maxlen=" + strconv.Itoa(p.MaxLength) + ":prompts=" + string(p.PromptScheme)
}

func (p FastEmbedProfile) ArchiveURL() string {
	return "https://storage.googleapis.com/qdrant-fastembed/" + p.Name + ".tar.gz"
}

// FastEmbedProfiles returns the closed set of bundled models. Default first,
// then the other five in vendor ListSupportedModels order.
func FastEmbedProfiles() []FastEmbedProfile {
	return []FastEmbedProfile{
		{
			Name:         "fast-bge-small-en-v1.5",
			Enum:         fastembed.BGESmallENV15,
			Dim:          384,
			MaxLength:    512,
			QueryPrefix:  bgeQueryInstruction,
			PromptScheme: PromptBGE,
			Description:  "Fast, default English model",
		},
		{
			Name:         "fast-all-MiniLM-L6-v2",
			Enum:         fastembed.AllMiniLML6V2,
			Dim:          384,
			MaxLength:    512,
			PromptScheme: PromptNone,
			Description:  "Sentence Transformer model, MiniLM-L6-v2",
		},
		{
			Name:         "fast-bge-base-en",
			Enum:         fastembed.BGEBaseEN,
			Dim:          768,
			MaxLength:    512,
			QueryPrefix:  bgeQueryInstruction,
			PromptScheme: PromptBGE,
			Description:  "Base English model",
		},
		{
			Name:         "fast-bge-base-en-v1.5",
			Enum:         fastembed.BGEBaseENV15,
			Dim:          768,
			MaxLength:    512,
			QueryPrefix:  bgeQueryInstruction,
			PromptScheme: PromptBGE,
			Description:  "v1.5 release of the base English model",
		},
		{
			Name:         "fast-bge-small-en",
			Enum:         fastembed.BGESmallEN,
			Dim:          384,
			MaxLength:    512,
			QueryPrefix:  bgeQueryInstruction,
			PromptScheme: PromptBGE,
			Description:  "Fast English model",
		},
		{
			Name:         "fast-bge-small-zh-v1.5",
			Enum:         fastembed.BGESmallZH,
			Dim:          512,
			MaxLength:    512,
			QueryPrefix:  "为这个句子生成表示以用于检索相关文章：",
			PromptScheme: PromptBGEZH,
			Description:  "Fast Chinese model",
		},
	}
}

func LookupFastEmbedProfile(name string) (FastEmbedProfile, bool) {
	if name == "" {
		return FastEmbedProfiles()[0], true
	}
	for _, p := range FastEmbedProfiles() {
		if p.Name == name {
			return p, true
		}
	}
	return FastEmbedProfile{}, false
}

func KnownFastEmbedModel(name string) bool {
	_, ok := LookupFastEmbedProfile(name)
	return ok
}
