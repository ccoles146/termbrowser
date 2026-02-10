package auth

import (
	"errors"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

var errInvalidCredentials = errors.New("invalid credentials")

type Manager struct {
	passwordHash []byte
	totpSecret   string
	jwtSecret    []byte
}

func NewManager(passwordHash, totpSecret string, jwtSecret []byte) *Manager {
	return &Manager{
		passwordHash: []byte(passwordHash),
		totpSecret:   totpSecret,
		jwtSecret:    jwtSecret,
	}
}

func (m *Manager) Verify(password, totpCode string) error {
	pwErr := bcrypt.CompareHashAndPassword(m.passwordHash, []byte(password))
	totpOK := totp.Validate(totpCode, m.totpSecret)
	if pwErr != nil || !totpOK {
		return errInvalidCredentials
	}
	return nil
}

func (m *Manager) IssueToken() (string, error) {
	claims := jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.jwtSecret)
}

func (m *Manager) SetCookie(w http.ResponseWriter, tokenStr string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "tb_session",
		Value:    tokenStr,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400,
		Path:     "/",
	})
}

func (m *Manager) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:   "tb_session",
		Value:  "",
		MaxAge: -1,
		Path:   "/",
	})
}

func (m *Manager) ValidateRequest(r *http.Request) error {
	cookie, err := r.Cookie("tb_session")
	if err != nil {
		return errInvalidCredentials
	}
	token, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errInvalidCredentials
		}
		return m.jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return errInvalidCredentials
	}
	return nil
}

func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := m.ValidateRequest(r); err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
