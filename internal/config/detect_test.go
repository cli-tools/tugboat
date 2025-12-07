package config

import (
	"testing"
)

func TestDetectVersion_V1FromGiteaURL(t *testing.T) {
	data := []byte(`{
		"gitea_url": "https://gitea.example.com",
		"gitea_token": "token",
		"organizations": []
	}`)

	version, err := DetectVersion(data)
	if err != nil {
		t.Fatalf("DetectVersion() error = %v", err)
	}
	if version != 1 {
		t.Errorf("DetectVersion() = %d, want 1", version)
	}
}

func TestDetectVersion_V2FromProviders(t *testing.T) {
	data := []byte(`{
		"providers": {"gitea": {}},
		"targets": []
	}`)

	version, err := DetectVersion(data)
	if err != nil {
		t.Fatalf("DetectVersion() error = %v", err)
	}
	if version != 2 {
		t.Errorf("DetectVersion() = %d, want 2", version)
	}
}

func TestDetectVersion_V2WithExplicitVersion(t *testing.T) {
	data := []byte(`{
		"version": 2,
		"providers": {"gitea": {}},
		"targets": []
	}`)

	version, err := DetectVersion(data)
	if err != nil {
		t.Fatalf("DetectVersion() error = %v", err)
	}
	if version != 2 {
		t.Errorf("DetectVersion() = %d, want 2", version)
	}
}

func TestDetectVersion_UnknownFormat(t *testing.T) {
	data := []byte(`{
		"something": "else"
	}`)

	_, err := DetectVersion(data)
	if err == nil {
		t.Error("DetectVersion() should return error for unknown format")
	}
}

func TestDetectVersion_InvalidJSON(t *testing.T) {
	data := []byte(`{invalid}`)

	_, err := DetectVersion(data)
	if err == nil {
		t.Error("DetectVersion() should return error for invalid JSON")
	}
}

func TestDetectVersion_EmptyObject(t *testing.T) {
	data := []byte(`{}`)

	_, err := DetectVersion(data)
	if err == nil {
		t.Error("DetectVersion() should return error for empty object")
	}
}

func TestDetectVersion_PrefersV2OverV1(t *testing.T) {
	// Edge case: if somehow both keys exist, prefer V2
	data := []byte(`{
		"gitea_url": "https://gitea.example.com",
		"providers": {"gitea": {}}
	}`)

	version, err := DetectVersion(data)
	if err != nil {
		t.Fatalf("DetectVersion() error = %v", err)
	}
	if version != 2 {
		t.Errorf("DetectVersion() = %d, want 2 (should prefer V2)", version)
	}
}
