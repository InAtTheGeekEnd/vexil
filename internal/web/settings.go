package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
)

// settingsContent is the data of the Settings page.
type settingsContent struct {
	Name      string
	Accent    string
	PoweredBy bool
	LogoURL   string // "" when the built-in logo is in use
	Errors    map[string]string
}

func (s *Server) settingsFromBrand() settingsContent {
	b := s.currentBrand()
	return settingsContent{Name: b.Name, Accent: b.Accent, PoweredBy: b.PoweredBy, LogoURL: b.LogoURL, Errors: map[string]string{}}
}

func (s *Server) renderSettings(w http.ResponseWriter, status int, c settingsContent, notice, errText string) {
	s.render(w, status, "settings.html", pageData{Content: c, Nav: "settings", Notice: notice, Error: errText})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, http.StatusOK, s.settingsFromBrand(), "", "")
}

// handleSettingsSave saves the brand. The form is multipart because of the
// logo file. The body limit leaves room for the other fields.
func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, brand.MaxLogoSize+64*1024)
	if err := r.ParseMultipartForm(brand.MaxLogoSize); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		c := s.settingsFromBrand()
		c.Errors["logo"] = capitalize(brand.ErrLogoTooLarge.Error()) + "."
		s.renderSettings(w, http.StatusBadRequest, c, "", "")
		return
	}
	c := settingsContent{
		Name:      strings.TrimSpace(r.FormValue("name")),
		Accent:    strings.TrimSpace(r.FormValue("accent")),
		PoweredBy: r.FormValue("powered_by") != "",
		LogoURL:   s.currentBrand().LogoURL,
		Errors:    map[string]string{},
	}
	b := s.currentBrand().Brand
	switch {
	case c.Name == "":
		c.Errors["name"] = "Enter a product name."
	case len([]rune(c.Name)) > brand.MaxNameLen:
		c.Errors["name"] = "Use at most 40 characters."
	default:
		b.Name = c.Name
	}
	if accent, err := brand.ParseAccent(c.Accent); err != nil {
		c.Errors["accent"] = capitalize(err.Error()) + "."
	} else {
		b.Accent = accent
	}
	b.PoweredBy = c.PoweredBy

	if file, _, err := r.FormFile("logo"); err == nil {
		defer file.Close()
		data, err := brand.ReadLogo(file)
		if err == nil {
			b.Logo, err = brand.ParseLogo(data)
		}
		if err != nil {
			c.Errors["logo"] = capitalize(err.Error()) + "."
		}
	} else if !errors.Is(err, http.ErrMissingFile) {
		c.Errors["logo"] = capitalize(brand.ErrLogoTooLarge.Error()) + "."
	}

	if len(c.Errors) > 0 {
		s.renderSettings(w, http.StatusBadRequest, c, "", "")
		return
	}
	if err := s.saveBrand(r.Context(), b); err != nil {
		s.serverError(w, err)
		return
	}
	s.renderSettings(w, http.StatusOK, s.settingsFromBrand(), "Settings saved.", "")
}

func (s *Server) handleLogoDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.deleteLogo(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	s.renderSettings(w, http.StatusOK, s.settingsFromBrand(), "Logo removed. The built-in logo is back.", "")
}

// handlePasswordChange sets a new password and logs out every other
// session. The current session stays.
func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	password := r.FormValue("password")
	if err := ValidateNewPassword(password, r.FormValue("confirm")); err != nil {
		c := s.settingsFromBrand()
		c.Errors["password"] = capitalize(err.Error()) + "."
		s.renderSettings(w, http.StatusBadRequest, c, "", "")
		return
	}
	hash, err := HashPassword(password)
	if err != nil {
		s.serverError(w, err)
		return
	}
	var keep string
	if cookie, err := r.Cookie(sessionCookie); err == nil && cookie.Value != "" {
		keep = hashToken(cookie.Value)
	}
	if err := s.store.SetPasswordHash(r.Context(), hash, keep); err != nil {
		s.serverError(w, err)
		return
	}
	s.renderSettings(w, http.StatusOK, s.settingsFromBrand(), "Password changed. Every other session was logged out.", "")
}
