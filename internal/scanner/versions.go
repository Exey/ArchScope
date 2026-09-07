package scanner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/exey/archscope/internal/langspec"
)

// Version is one detected technology paired with the version resolved for it
// from a project manifest (package.json, go.mod, Cargo.toml, …). It is the unit
// the architecture panel's "Versions" block renders.
type Version struct {
	Name     string // display label, e.g. "React", "TypeScript", "Go"
	Version  string // resolved version string, e.g. "19.2", "1.23", "5.9.3"
	Category string // one of versionCategoryOrder below
	Source   string // manifest file it was read from, e.g. "package.json"
}

// versionCategoryOrder is the render/sort order for Version.Category. Runtime and
// language sit first (what the code runs on), then the toolchain, then libraries.
var versionCategoryOrder = []string{
	"language", "runtime", "package-manager", "build", "framework", "testing", "library",
}

func categoryRank(c string) int {
	for i, name := range versionCategoryOrder {
		if name == c {
			return i
		}
	}
	return len(versionCategoryOrder)
}

// DetectVersions walks rootPath for per-ecosystem manifest files and returns the
// detected technology versions grouped by report-tab platform. It is a pure
// filesystem read — safe to call independently of the rest of the scan — and is
// deliberately curated: runtime/language, package manager, build tool and the
// well-known frameworks, not the full dependency closure.
func DetectVersions(rootPath string) map[langspec.Platform][]Version {
	accs := map[langspec.Platform]*versionAcc{}
	acc := func(p langspec.Platform) *versionAcc {
		if accs[p] == nil {
			accs[p] = &versionAcc{seen: map[string]bool{}}
		}
		return accs[p]
	}

	walkManifests(rootPath, func(path string) {
		switch filepath.Base(path) {
		case "package.json":
			detectNode(path, acc(langspec.PlatformTSJS))
		case "pnpm-lock.yaml", "yarn.lock", "package-lock.json":
			detectJSLockfile(path, acc(langspec.PlatformTSJS))
		case ".nvmrc", ".node-version", ".tool-versions":
			detectNodeVersionFile(path, acc(langspec.PlatformTSJS))
		case "go.mod":
			detectGoMod(path, acc(langspec.PlatformGo))
		case "pyproject.toml", "requirements.txt", "Pipfile":
			detectPython(path, acc(langspec.PlatformPython))
		case "Cargo.toml":
			detectCargo(path, acc(langspec.PlatformRust))
		case "Package.swift":
			detectSwiftPackage(path, acc(langspec.PlatformSwiftObjC))
		case "Package.resolved":
			detectSwiftResolved(path, acc(langspec.PlatformSwiftObjC))
		case "Podfile.lock":
			detectPodfileLock(path, acc(langspec.PlatformSwiftObjC))
		case "build.gradle", "build.gradle.kts", "settings.gradle.kts":
			detectGradle(path, acc(langspec.PlatformKotlin), acc(langspec.PlatformJava))
		case "libs.versions.toml":
			detectGradleVersionCatalog(path, acc(langspec.PlatformKotlin))
		case "pom.xml":
			detectMavenPom(path, acc(langspec.PlatformJava))
		case "CMakeLists.txt":
			detectCMake(path, acc(langspec.PlatformC))
		}
	})

	out := map[langspec.Platform][]Version{}
	for p, a := range accs {
		if len(a.list) == 0 {
			continue
		}
		sort.SliceStable(a.list, func(i, j int) bool {
			ri, rj := categoryRank(a.list[i].Category), categoryRank(a.list[j].Category)
			if ri != rj {
				return ri < rj
			}
			return a.list[i].Name < a.list[j].Name
		})
		out[p] = a.list
	}
	return out
}

// versionAcc collects Version entries for one platform, first-write-wins per name.
type versionAcc struct {
	list []Version
	seen map[string]bool
}

func (a *versionAcc) add(name, version, category, source string) {
	version = cleanVersion(version)
	if name == "" || version == "" || a.seen[name] {
		return
	}
	a.seen[name] = true
	a.list = append(a.list, Version{Name: name, Version: version, Category: category, Source: source})
}

// walkManifests calls fn for every manifest-looking file under root, at most a
// few levels deep, skipping vendored / build-output / VCS directories.
func walkManifests(root string, fn func(path string)) {
	const maxDepth = 4
	skipDir := map[string]bool{
		"node_modules": true, "vendor": true, "target": true, "dist": true,
		"build": true, "out": true, ".build": true, "testdata": true,
		"Pods": true, "DerivedData": true, "__pycache__": true, ".venv": true,
		"venv": true, "Carthage": true, "bin": true,
	}
	manifest := map[string]bool{
		"package.json": true, "go.mod": true, "pyproject.toml": true,
		"requirements.txt": true, "Pipfile": true, "Cargo.toml": true,
		"Package.swift": true, "Package.resolved": true, "Podfile.lock": true,
		"build.gradle": true, "build.gradle.kts": true, "settings.gradle.kts": true,
		"libs.versions.toml": true, "pom.xml": true, "CMakeLists.txt": true,
		"pnpm-lock.yaml": true, "yarn.lock": true, "package-lock.json": true,
		".nvmrc": true, ".node-version": true, ".tool-versions": true,
	}

	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				if depth >= maxDepth || strings.HasPrefix(name, ".") || skipDir[name] {
					continue
				}
				walk(filepath.Join(dir, name), depth+1)
				continue
			}
			if manifest[name] {
				fn(filepath.Join(dir, name))
			}
		}
	}
	walk(root, 0)
}

// cleanVersion normalizes a raw version / range token to a bare version, or ""
// when the token carries no usable version (workspace refs, git URLs, "*", …).
func cleanVersion(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	s = strings.TrimLeft(s, "^~>=< v")
	if i := strings.IndexAny(s, " ,|+"); i >= 0 {
		s = s[:i]
	}
	switch {
	case s == "", s == "*":
		return ""
	case strings.HasPrefix(s, "workspace"), strings.HasPrefix(s, "catalog"),
		strings.HasPrefix(s, "link"), strings.HasPrefix(s, "file"),
		strings.HasPrefix(s, "git"), strings.HasPrefix(s, "http"),
		strings.HasPrefix(s, "npm:"):
		return ""
	}
	if !strings.ContainsAny(s, "0123456789") {
		return ""
	}
	return s
}

// ── Node / TypeScript ────────────────────────────────────────────────────────

type nodeManifest struct {
	Engines         map[string]string `json:"engines"`
	PackageManager  string            `json:"packageManager"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

var npmKnown = []struct{ key, name, category string }{
	{"typescript", "TypeScript", "language"},
	{"vite", "Vite", "build"},
	{"webpack", "webpack", "build"},
	{"@rspack/core", "Rspack", "build"},
	{"esbuild", "esbuild", "build"},
	{"rollup", "Rollup", "build"},
	{"parcel", "Parcel", "build"},
	{"turbo", "Turborepo", "build"},
	{"next", "Next.js", "framework"},
	{"react", "React", "framework"},
	{"react-dom", "React DOM", "framework"},
	{"vue", "Vue", "framework"},
	{"nuxt", "Nuxt", "framework"},
	{"@angular/core", "Angular", "framework"},
	{"svelte", "Svelte", "framework"},
	{"@sveltejs/kit", "SvelteKit", "framework"},
	{"solid-js", "Solid", "framework"},
	{"astro", "Astro", "framework"},
	{"@remix-run/react", "Remix", "framework"},
	{"express", "Express", "framework"},
	{"@nestjs/core", "NestJS", "framework"},
	{"fastify", "Fastify", "framework"},
	{"react-router-dom", "React Router", "library"},
	{"@tanstack/react-query", "TanStack Query", "library"},
	{"react-query", "React Query", "library"},
	{"@reduxjs/toolkit", "Redux Toolkit", "library"},
	{"redux", "Redux", "library"},
	{"zustand", "Zustand", "library"},
	{"mobx", "MobX", "library"},
	{"tailwindcss", "Tailwind CSS", "library"},
	{"zod", "Zod", "library"},
	{"react-hook-form", "React Hook Form", "library"},
	{"jest", "Jest", "testing"},
	{"vitest", "Vitest", "testing"},
	{"@playwright/test", "Playwright", "testing"},
	{"cypress", "Cypress", "testing"},
}

func detectNode(path string, a *versionAcc) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var m nodeManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return
	}
	if v := m.Engines["node"]; v != "" {
		a.add("Node", v, "runtime", "package.json")
	}
	if m.PackageManager != "" {
		if name, ver, ok := strings.Cut(m.PackageManager, "@"); ok {
			a.add(name, ver, "package-manager", "package.json")
		}
	}
	lookup := func(key string) string {
		if v, ok := m.Dependencies[key]; ok {
			return v
		}
		return m.DevDependencies[key]
	}
	for _, k := range npmKnown {
		if v := lookup(k.key); v != "" {
			a.add(k.name, v, k.category, "package.json")
		}
	}
}

// pnpmLockfileMajor maps a pnpm-lock.yaml lockfileVersion to the pnpm major that
// writes it. Only the format major is knowable from the lockfile — the exact
// pnpm version is not recorded anywhere unless package.json declares it.
var pnpmLockfileMajor = map[string]string{
	"9.0": "9", "6.1": "8", "6.0": "8", "5.4": "7", "5.3": "6", "5.2": "5", "5.1": "5", "5.0": "5",
}

// npmLockfileMajor maps package-lock.json lockfileVersion to an approximate npm major.
var npmLockfileMajor = map[int]string{3: "9", 2: "7", 1: "6"}

var (
	pnpmLockVerRe = regexp.MustCompile(`lockfileVersion:\s*'?"?([0-9]+\.[0-9]+)`)
	npmLockVerRe  = regexp.MustCompile(`"lockfileVersion":\s*([0-9]+)`)
	yarnMetaRe    = regexp.MustCompile(`(?m)^__metadata:`)
)

// detectJSLockfile infers the JS package manager and an approximate major
// version from a lockfile when package.json carries no `packageManager` field.
// package.json is walked first in a directory, so an explicit version wins.
func detectJSLockfile(path string, a *versionAcc) {
	if a.seen["pnpm"] || a.seen["yarn"] || a.seen["npm"] {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	src := string(raw)
	switch filepath.Base(path) {
	case "pnpm-lock.yaml":
		ver := ""
		if m := pnpmLockVerRe.FindStringSubmatch(src); m != nil {
			ver = pnpmLockfileMajor[m[1]]
		}
		a.add("pnpm", ver, "package-manager", "pnpm-lock.yaml (lockfile-inferred)")
	case "package-lock.json":
		ver := ""
		if m := npmLockVerRe.FindStringSubmatch(src); m != nil {
			n, _ := strconv.Atoi(m[1])
			ver = npmLockfileMajor[n]
		}
		a.add("npm", ver, "package-manager", "package-lock.json (lockfile-inferred)")
	case "yarn.lock":
		if yarnMetaRe.MatchString(src) {
			return // Yarn Berry (2+) — exact major not recoverable from the lockfile
		}
		a.add("Yarn", "1", "package-manager", "yarn.lock (lockfile-inferred)")
	}
}

// detectNodeVersionFile reads an exact Node version from .nvmrc / .node-version,
// or the nodejs/pnpm/yarn line of an asdf/mise .tool-versions file.
func detectNodeVersionFile(path string, a *versionAcc) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	base := filepath.Base(path)
	if base == ".tool-versions" {
		for _, line := range strings.Split(string(raw), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			switch strings.ToLower(f[0]) {
			case "nodejs", "node":
				a.add("Node", f[1], "runtime", ".tool-versions")
			case "pnpm":
				a.add("pnpm", f[1], "package-manager", ".tool-versions")
			case "yarn":
				a.add("Yarn", f[1], "package-manager", ".tool-versions")
			}
		}
		return
	}
	a.add("Node", strings.TrimSpace(string(raw)), "runtime", base)
}

// ── Go ───────────────────────────────────────────────────────────────────────

var goKnown = map[string]string{
	"github.com/gin-gonic/gin":            "Gin",
	"github.com/labstack/echo":            "Echo",
	"github.com/gofiber/fiber":            "Fiber",
	"github.com/go-chi/chi":               "Chi",
	"github.com/gorilla/mux":              "Gorilla Mux",
	"gorm.io/gorm":                        "GORM",
	"entgo.io/ent":                        "Ent",
	"github.com/jmoiron/sqlx":             "sqlx",
	"google.golang.org/grpc":              "gRPC",
	"github.com/spf13/cobra":              "Cobra",
	"github.com/99designs/gqlgen":         "gqlgen",
	"github.com/prometheus/client_golang": "Prometheus",
	"go.uber.org/zap":                     "Zap",
}

func detectGoMod(path string, a *versionAcc) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	inRequire := false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.HasPrefix(line, "go ") {
			a.add("Go", strings.TrimSpace(line[3:]), "language", "go.mod")
			continue
		}
		switch {
		case strings.HasPrefix(line, "require ("):
			inRequire = true
			continue
		case line == ")":
			inRequire = false
			continue
		case strings.HasPrefix(line, "require "):
			line = strings.TrimPrefix(line, "require ")
		case !inRequire:
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		mod, ver := fields[0], fields[1]
		for prefix, name := range goKnown {
			if strings.HasPrefix(mod, prefix) {
				a.add(name, ver, "framework", "go.mod")
				break
			}
		}
	}
}

// ── Python ───────────────────────────────────────────────────────────────────

var pyKnown = map[string]string{
	"django": "Django", "flask": "Flask", "fastapi": "FastAPI",
	"sqlalchemy": "SQLAlchemy", "pydantic": "Pydantic", "celery": "Celery",
	"uvicorn": "Uvicorn", "gunicorn": "Gunicorn", "numpy": "NumPy",
	"pandas": "pandas", "torch": "PyTorch", "tensorflow": "TensorFlow",
	"scikit-learn": "scikit-learn", "pytest": "pytest", "aiohttp": "aiohttp",
	"httpx": "HTTPX", "starlette": "Starlette",
}

var (
	pyReqPython = regexp.MustCompile(`requires-python\s*=\s*"[^0-9]*([0-9]+(?:\.[0-9]+)*)`)
	// name<op>version — requirements.txt lines and PEP 621 inline arrays alike.
	pyDepSpec = regexp.MustCompile(`([A-Za-z0-9_.-]+)\s*(?:[=~!<>]=|[<>])\s*([0-9]+(?:\.[0-9]+)*)`)
	// name = "spec" — Poetry / Pipfile section entries.
	pyDepAssign  = regexp.MustCompile(`^([A-Za-z0-9_.-]+)\s*=\s*"[^0-9"]*([0-9]+(?:\.[0-9]+)*)`)
	pyTOMLHeader = regexp.MustCompile(`^\[([a-zA-Z0-9_.-]+)\]`)
)

func detectPython(path string, a *versionAcc) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	src := string(raw)
	base := filepath.Base(path)
	if base == "pyproject.toml" || base == "Pipfile" {
		if m := pyReqPython.FindStringSubmatch(src); m != nil {
			a.add("Python", m[1], "language", base)
		}
	}

	addDep := func(rawName, ver string) {
		if name, ok := pyKnown[strings.ToLower(rawName)]; ok {
			a.add(name, ver, categoryForPy(name), base)
		}
	}
	for _, m := range pyDepSpec.FindAllStringSubmatch(src, -1) {
		if strings.EqualFold(m[1], "python") || strings.HasSuffix(strings.ToLower(m[1]), "-python") {
			continue
		}
		addDep(m[1], m[2])
	}
	section := ""
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if h := pyTOMLHeader.FindStringSubmatch(line); h != nil {
			section = h[1]
			continue
		}
		if strings.Contains(section, "dependencies") || section == "packages" || section == "dev-packages" {
			if m := pyDepAssign.FindStringSubmatch(line); m != nil {
				addDep(m[1], m[2])
			}
		}
	}
}

func categoryForPy(name string) string {
	switch name {
	case "Django", "Flask", "FastAPI", "Starlette", "aiohttp":
		return "framework"
	case "pytest":
		return "testing"
	default:
		return "library"
	}
}

// ── Rust ─────────────────────────────────────────────────────────────────────

var rustKnown = map[string]string{
	"tokio": "Tokio", "actix-web": "Actix Web", "axum": "Axum",
	"rocket": "Rocket", "serde": "Serde", "diesel": "Diesel",
	"sqlx": "SQLx", "tonic": "Tonic", "hyper": "Hyper",
	"clap": "clap", "warp": "warp", "reqwest": "reqwest",
}

var (
	rustSectionRe = regexp.MustCompile(`^\[([a-zA-Z0-9_.-]+)\]`)
	rustDepRe     = regexp.MustCompile(`^([A-Za-z0-9_-]+)\s*=\s*(?:"([^"]+)"|\{[^}]*version\s*=\s*"([^"]+)")`)
	rustKVRe      = regexp.MustCompile(`^(edition|rust-version)\s*=\s*"([^"]+)"`)
)

func detectCargo(path string, a *versionAcc) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if m := rustSectionRe.FindStringSubmatch(line); m != nil {
			section = m[1]
			continue
		}
		if section == "package" {
			if m := rustKVRe.FindStringSubmatch(line); m != nil {
				if m[1] == "edition" {
					a.add("Rust Edition", m[2], "language", "Cargo.toml")
				} else {
					a.add("Rust", m[2], "language", "Cargo.toml")
				}
			}
			continue
		}
		if section == "dependencies" || section == "dev-dependencies" {
			if m := rustDepRe.FindStringSubmatch(line); m != nil {
				name, ok := rustKnown[m[1]]
				if !ok {
					continue
				}
				ver := m[2]
				if ver == "" {
					ver = m[3]
				}
				cat := "library"
				if m[1] == "actix-web" || m[1] == "axum" || m[1] == "rocket" || m[1] == "warp" {
					cat = "framework"
				}
				a.add(name, ver, cat, "Cargo.toml")
			}
		}
	}
}

// ── Swift ────────────────────────────────────────────────────────────────────

var swiftToolsRe = regexp.MustCompile(`swift-tools-version:\s*([0-9]+(?:\.[0-9]+)*)`)

func detectSwiftPackage(path string, a *versionAcc) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if m := swiftToolsRe.FindStringSubmatch(string(raw)); m != nil {
		a.add("Swift Tools", m[1], "language", "Package.swift")
	}
}

var swiftPkgKnown = map[string]string{
	"alamofire": "Alamofire", "swift-nio": "SwiftNIO", "vapor": "Vapor",
	"swift-composable-architecture": "TCA", "rxswift": "RxSwift",
	"snapkit": "SnapKit", "kingfisher": "Kingfisher", "swift-collections": "Swift Collections",
	"swift-syntax": "SwiftSyntax", "swinject": "Swinject",
}

type swiftResolved struct {
	Pins []struct {
		Identity string `json:"identity"`
		Location string `json:"location"`
		State    struct {
			Version string `json:"version"`
		} `json:"state"`
	} `json:"pins"`
	Object struct {
		Pins []struct {
			Package       string `json:"package"`
			RepositoryURL string `json:"repositoryURL"`
			State         struct {
				Version string `json:"version"`
			} `json:"state"`
		} `json:"pins"`
	} `json:"object"`
}

func detectSwiftResolved(path string, a *versionAcc) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var r swiftResolved
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	match := func(id, ver string) {
		id = strings.ToLower(id)
		for key, name := range swiftPkgKnown {
			if strings.Contains(id, key) {
				a.add(name, ver, "library", "Package.resolved")
				return
			}
		}
	}
	for _, p := range r.Pins { // v2 format
		match(p.Identity+" "+p.Location, p.State.Version)
	}
	for _, p := range r.Object.Pins { // v1 format
		match(p.Package+" "+p.RepositoryURL, p.State.Version)
	}
}

var podLineRe = regexp.MustCompile(`^-\s+"?([A-Za-z0-9_+/.-]+)"?\s+\(([0-9][0-9A-Za-z.+-]*)\)`)

var podKnown = map[string]string{
	"alamofire": "Alamofire", "rxswift": "RxSwift", "snapkit": "SnapKit",
	"kingfisher": "Kingfisher", "realm": "Realm", "firebase": "Firebase",
	"swiftygif": "SwiftyGif", "lottie-ios": "Lottie",
}

func detectPodfileLock(path string, a *versionAcc) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		m := podLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		base := strings.ToLower(m[1])
		if i := strings.IndexByte(base, '/'); i >= 0 {
			base = base[:i]
		}
		if name, ok := podKnown[base]; ok {
			a.add(name, m[2], "library", "Podfile.lock")
		}
	}
}

// ── Kotlin / Java (Gradle + Maven) ───────────────────────────────────────────

var (
	gradleKotlinRe   = regexp.MustCompile(`kotlin(?:\("[^"]+"\)|\s*\(\s*"[^"]+"\s*\))\s*version\s*"([0-9][0-9.]*)"`)
	gradleKotlinIDRe = regexp.MustCompile(`id\("org\.jetbrains\.kotlin\.[^"]+"\)\s*version\s*"([0-9][0-9.]*)"`)
	gradleSpringRe   = regexp.MustCompile(`id\("org\.springframework\.boot"\)\s*version\s*"([0-9][0-9.]*)"`)
	gradleJavaRe     = regexp.MustCompile(`(?:sourceCompatibility|targetCompatibility|languageVersion)\s*[=(]\s*(?:JavaVersion\.VERSION_|JavaLanguageVersion\.of\()?["']?([0-9]+(?:\.[0-9]+)?)`)
)

func detectGradle(path string, kotlin, java *versionAcc) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	src := string(raw)
	base := filepath.Base(path)
	if m := gradleKotlinRe.FindStringSubmatch(src); m != nil {
		kotlin.add("Kotlin", m[1], "language", base)
	} else if m := gradleKotlinIDRe.FindStringSubmatch(src); m != nil {
		kotlin.add("Kotlin", m[1], "language", base)
	}
	if m := gradleSpringRe.FindStringSubmatch(src); m != nil {
		kotlin.add("Spring Boot", m[1], "framework", base)
		java.add("Spring Boot", m[1], "framework", base)
	}
	if m := gradleJavaRe.FindStringSubmatch(src); m != nil {
		java.add("Java", m[1], "language", base)
	}
}

var versionCatalogLineRe = regexp.MustCompile(`^([A-Za-z0-9_-]+)\s*=\s*"([0-9][0-9.]*)"`)

func detectGradleVersionCatalog(path string, kotlin *versionAcc) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	inVersions := false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inVersions = line == "[versions]"
			continue
		}
		if !inVersions {
			continue
		}
		if m := versionCatalogLineRe.FindStringSubmatch(line); m != nil {
			switch strings.ToLower(m[1]) {
			case "kotlin":
				kotlin.add("Kotlin", m[2], "language", "libs.versions.toml")
			case "springboot", "spring-boot":
				kotlin.add("Spring Boot", m[2], "framework", "libs.versions.toml")
			}
		}
	}
}

var (
	pomJavaRe   = regexp.MustCompile(`<(?:java\.version|maven\.compiler\.(?:source|release))>([0-9]+(?:\.[0-9]+)?)</`)
	pomKotlinRe = regexp.MustCompile(`<kotlin\.version>([0-9][0-9.]*)</`)
	pomParentRe = regexp.MustCompile(`(?s)<parent>.*?<artifactId>spring-boot-starter-parent</artifactId>.*?<version>([0-9][0-9.]*)</version>.*?</parent>`)
	pomSpringRe = regexp.MustCompile(`<spring-boot\.version>([0-9][0-9.]*)</`)
)

func detectMavenPom(path string, java *versionAcc) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	src := string(raw)
	if m := pomJavaRe.FindStringSubmatch(src); m != nil {
		java.add("Java", m[1], "language", "pom.xml")
	}
	if m := pomKotlinRe.FindStringSubmatch(src); m != nil {
		java.add("Kotlin", m[1], "language", "pom.xml")
	}
	if m := pomParentRe.FindStringSubmatch(src); m != nil {
		java.add("Spring Boot", m[1], "framework", "pom.xml")
	} else if m := pomSpringRe.FindStringSubmatch(src); m != nil {
		java.add("Spring Boot", m[1], "framework", "pom.xml")
	}
}

// ── C / C++ ──────────────────────────────────────────────────────────────────

var (
	cmakeCxxStdRe = regexp.MustCompile(`(?i)set\s*\(\s*CMAKE_CXX_STANDARD\s+([0-9]+)`)
	cmakeCStdRe   = regexp.MustCompile(`(?i)set\s*\(\s*CMAKE_C_STANDARD\s+([0-9]+)`)
	cmakeMinRe    = regexp.MustCompile(`(?i)cmake_minimum_required\s*\(\s*VERSION\s+([0-9]+(?:\.[0-9]+)*)`)
)

func detectCMake(path string, a *versionAcc) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	src := string(raw)
	if m := cmakeCxxStdRe.FindStringSubmatch(src); m != nil {
		a.add("C++ Standard", m[1], "language", "CMakeLists.txt")
	}
	if m := cmakeCStdRe.FindStringSubmatch(src); m != nil {
		a.add("C Standard", m[1], "language", "CMakeLists.txt")
	}
	if m := cmakeMinRe.FindStringSubmatch(src); m != nil {
		a.add("CMake", m[1], "build", "CMakeLists.txt")
	}
}
