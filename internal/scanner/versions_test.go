package scanner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/exey/archscope/internal/langspec"
)

func writeManifest(t *testing.T, dir, name, body string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findVersion(vs []Version, name string) (Version, bool) {
	for _, v := range vs {
		if v.Name == name {
			return v, true
		}
	}
	return Version{}, false
}

func TestDetectVersionsNode(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "package.json", `{
	  "packageManager": "pnpm@9.1.0+sha512.abcdef",
	  "engines": { "node": ">=20" },
	  "dependencies": {
	    "react": "19.2.0",
	    "react-router-dom": "^7.1.0",
	    "@tanstack/react-query": "5.90.1",
	    "zod": "workspace:*"
	  },
	  "devDependencies": {
	    "typescript": "~5.9.2",
	    "vite": "7.0.0"
	  }
	}`)

	got := DetectVersions(dir)
	vs := got[langspec.PlatformTSJS]
	if len(vs) == 0 {
		t.Fatal("no TS/JS versions detected")
	}
	cases := map[string]string{
		"Node":           "20",
		"pnpm":           "9.1.0",
		"React":          "19.2.0",
		"React Router":   "7.1.0",
		"TanStack Query": "5.90.1",
		"TypeScript":     "5.9.2",
		"Vite":           "7.0.0",
	}
	for name, want := range cases {
		v, ok := findVersion(vs, name)
		if !ok {
			t.Errorf("%s not detected", name)
			continue
		}
		if v.Version != want {
			t.Errorf("%s: version = %q, want %q", name, v.Version, want)
		}
	}
	if _, ok := findVersion(vs, "Zod"); ok {
		t.Error("Zod should be skipped (workspace:* has no version)")
	}
	// language category sorts before framework/library.
	if vs[0].Category != "language" && vs[0].Category != "runtime" {
		t.Errorf("first entry category = %q, want language/runtime", vs[0].Category)
	}
}

func TestDetectVersionsLockfileFallback(t *testing.T) {
	dir := t.TempDir()
	// package.json with no packageManager / engines field.
	writeManifest(t, dir, "frontend/package.json", `{"dependencies":{"react":"^19.2.0"}}`)
	writeManifest(t, dir, "frontend/pnpm-lock.yaml", "lockfileVersion: '9.0'\n\nimporters:\n  .:\n")
	writeManifest(t, dir, "frontend/.nvmrc", "20.11.1\n")

	vs := DetectVersions(dir)[langspec.PlatformTSJS]
	if v, ok := findVersion(vs, "pnpm"); !ok || v.Version != "9" {
		t.Errorf("pnpm: got %q/%v, want major 9 from lockfileVersion", v.Version, ok)
	}
	if v, ok := findVersion(vs, "Node"); !ok || v.Version != "20.11.1" {
		t.Errorf("Node: got %q/%v, want 20.11.1 from .nvmrc", v.Version, ok)
	}
}

func TestDetectVersionsPackageManagerFieldWins(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "package.json", `{"packageManager":"pnpm@9.12.0"}`)
	writeManifest(t, dir, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	vs := DetectVersions(dir)[langspec.PlatformTSJS]
	if v, _ := findVersion(vs, "pnpm"); v.Version != "9.12.0" {
		t.Errorf("pnpm: got %q, want exact 9.12.0 (packageManager field beats lockfile)", v.Version)
	}
}

func TestDetectVersionsToolVersions(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "package.json", `{"dependencies":{"react":"19.0.0"}}`)
	writeManifest(t, dir, ".tool-versions", "nodejs 20.11.1\npnpm 9.12.0\ngolang 1.23.0\n")
	vs := DetectVersions(dir)[langspec.PlatformTSJS]
	if v, _ := findVersion(vs, "Node"); v.Version != "20.11.1" {
		t.Errorf("Node from .tool-versions: %q", v.Version)
	}
	if v, _ := findVersion(vs, "pnpm"); v.Version != "9.12.0" {
		t.Errorf("pnpm from .tool-versions: %q", v.Version)
	}
}

func TestDetectVersionsGoMod(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "go.mod", `module example.com/app

go 1.23

require (
	github.com/gin-gonic/gin v1.10.0
	google.golang.org/grpc v1.62.1
	github.com/spf13/cobra v1.8.0
)
`)
	vs := DetectVersions(dir)[langspec.PlatformGo]
	for name, want := range map[string]string{
		"Go": "1.23", "Gin": "1.10.0", "gRPC": "1.62.1", "Cobra": "1.8.0",
	} {
		v, ok := findVersion(vs, name)
		if !ok || v.Version != want {
			t.Errorf("%s: got %q/%v, want %q", name, v.Version, ok, want)
		}
	}
}

func TestDetectVersionsPythonAndCargo(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "svc/pyproject.toml", `[project]
requires-python = ">=3.11"
dependencies = ["django>=5.0.1", "pydantic==2.6.0"]
`)
	writeManifest(t, dir, "rs/Cargo.toml", `[package]
name = "x"
edition = "2021"
rust-version = "1.75"

[dependencies]
tokio = "1.36"
axum = { version = "0.7.4", features = ["macros"] }
`)
	got := DetectVersions(dir)

	py := got[langspec.PlatformPython]
	if v, ok := findVersion(py, "Python"); !ok || v.Version != "3.11" {
		t.Errorf("Python: %q/%v", v.Version, ok)
	}
	if v, ok := findVersion(py, "Django"); !ok || v.Version != "5.0.1" {
		t.Errorf("Django: %q/%v", v.Version, ok)
	}

	rs := got[langspec.PlatformRust]
	if v, ok := findVersion(rs, "Rust Edition"); !ok || v.Version != "2021" {
		t.Errorf("Rust Edition: %q/%v", v.Version, ok)
	}
	if v, ok := findVersion(rs, "Axum"); !ok || v.Version != "0.7.4" || v.Category != "framework" {
		t.Errorf("Axum: %q/%v/%q", v.Version, ok, v.Category)
	}
	if v, ok := findVersion(rs, "Tokio"); !ok || v.Version != "1.36" {
		t.Errorf("Tokio: %q/%v", v.Version, ok)
	}
}

func TestDetectVersionsSkipsVendored(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "node_modules/foo/package.json", `{"dependencies":{"react":"1.0.0"}}`)
	if len(DetectVersions(dir)) != 0 {
		t.Error("expected node_modules to be skipped")
	}
}

func TestCleanVersion(t *testing.T) {
	cases := map[string]string{
		"^19.2.0": "19.2.0", "~5.9": "5.9", ">= 3.11": "3.11",
		"v1.2.3": "1.2.3", "1.10.0": "1.10.0", "workspace:*": "",
		"*": "", "": "", "latest": "", "npm:pkg@1.0.0": "",
		"9.1.0+sha512.abc": "9.1.0", "1.36, <2": "1.36",
	}
	for in, want := range cases {
		if got := cleanVersion(in); got != want {
			t.Errorf("cleanVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
