package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

type Config struct {
	PasswordHash string `yaml:"password_hash"`
	TOTPSecret   string `yaml:"totp_secret"`
	Port         int    `yaml:"port"`
	JWTSecret    string `yaml:"jwt_secret"`
}

func DefaultPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "config.yaml"
	}
	return filepath.Join(filepath.Dir(exe), "config.yaml")
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if cfg.Port == 0 {
		cfg.Port = 8765
	}
	return &cfg, nil
}

func Save(cfg *Config, path string) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func RunFirstSetup(path string) (*Config, error) {
	fmt.Println("=== termbrowser first-run setup ===")

	fmt.Print("Enter password: ")
	pw1, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return nil, fmt.Errorf("reading password: %w", err)
	}

	fmt.Print("Confirm password: ")
	pw2, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return nil, fmt.Errorf("reading password: %w", err)
	}

	if string(pw1) != string(pw2) {
		return nil, fmt.Errorf("passwords do not match")
	}
	if len(pw1) == 0 {
		return nil, fmt.Errorf("password cannot be empty")
	}

	hash, err := bcrypt.GenerateFromPassword(pw1, 12)
	if err != nil {
		return nil, fmt.Errorf("hashing password: %w", err)
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "termbrowser",
		AccountName: "admin",
	})
	if err != nil {
		return nil, fmt.Errorf("generating TOTP: %w", err)
	}

	jwtBuf := make([]byte, 32)
	if _, err := rand.Read(jwtBuf); err != nil {
		return nil, fmt.Errorf("generating JWT secret: %w", err)
	}

	cfg := &Config{
		PasswordHash: string(hash),
		TOTPSecret:   key.Secret(),
		Port:         8765,
		JWTSecret:    hex.EncodeToString(jwtBuf),
	}

	if err := Save(cfg, path); err != nil {
		return nil, fmt.Errorf("saving config: %w", err)
	}

	fmt.Printf("\nTOTP Secret: %s\n", key.Secret())
	fmt.Printf("TOTP URI:    %s\n", key.URL())
	fmt.Println("\nScan the URI with your authenticator app (e.g. Google Authenticator, Authy).")
	fmt.Printf("Config saved to: %s\n\n", path)

	return cfg, nil
}
