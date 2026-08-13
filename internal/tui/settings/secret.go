package settings

import (
	"errors"
	"github.com/svnbjrn/spoon/internal/tui/theme"
	"strings"
	"unicode/utf8"
)

var ErrSecretClipboard = errors.New("credentials cannot be copied to the clipboard")

// SecretInput keeps a credential separate from every render string. Its mask is
// fixed-width so neither the value nor its length reaches the terminal.
type SecretInput struct{ value string }

func NewSecretInput() SecretInput          { return SecretInput{} }
func (s *SecretInput) Set(value string)    { s.value = value }
func (s *SecretInput) Append(value string) { s.value += value }
func (s *SecretInput) Clear()              { s.value = "" }
func (s SecretInput) Value() string        { return s.value }
func (s SecretInput) Render() string {
	return s.RenderWithTheme(theme.DefaultContext())
}

func (s SecretInput) RenderWithTheme(ctx theme.Context) string {
	if s.value == "" {
		return ""
	}
	return strings.Repeat(ctx.Glyph(theme.Selected), 12)
}
func (s SecretInput) Yank() error { return ErrSecretClipboard }
func (s *SecretInput) Backspace() {
	if s.value == "" {
		return
	}
	_, size := utf8.DecodeLastRuneInString(s.value)
	s.value = s.value[:len(s.value)-size]
}
