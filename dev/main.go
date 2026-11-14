// Command dev is Lanternfolk's task runner, written in Go so the project
// needs nothing but the go command: no Make, no shell scripts.
//
//	go run ./dev            # list tasks
//	go run ./dev ci         # everything CI runs
//	go run ./dev test -race
//
// Tools (staticcheck, govulncheck, goreleaser, asuka) are pinned in
// tools/go.mod. They are built from inside tools/ into bin/tools/, so the go
// command picks the toolchain tools/go.mod asks for, and rebuilt only when
// tools/go.mod or tools/go.sum change.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type task struct {
	help string
	run  func(args []string) error
}

var tasks map[string]task

func init() {
	tasks = map[string]task{
		"fmt":          {"fail if any file needs gofmt (-w to rewrite)", fmtTask},
		"vet":          {"go vet ./...", func([]string) error { return goCmd("vet", "./...") }},
		"test":         {"go test ./... (-race on platforms that support it)", testTask},
		"lint":         {"staticcheck for linux, darwin, and windows", lintTask},
		"vuln":         {"govulncheck ./...", func([]string) error { return tool("govulncheck", "./...") }},
		"build":        {"build every binary for every OS/arch with CGO_ENABLED=0", buildTask},
		"ci":           {"fmt, vet, lint, test, build, vuln", ciTask},
		"snapshot":     {"goreleaser snapshot build into dist/ (no publishing)", snapshotTask},
		"release":      {"goreleaser release (CI only; needs GITHUB_TOKEN and a tag)", releaseTask},
		"asuka":        {"the asuka CLI (at go.mod's version) on the app: asuka build -p ios --simulator", asukaTask},
		"desktop-dist": {"package the desktop app into dist/desktop (VERSION=v1.2.3, -platforms mac,windows,linux)", desktopDistTask},
		"tools":        {"build the pinned tools into bin/tools", func([]string) error { return errors.Join(ensureTools(), ensureAsuka()) }},
	}
}

// Every binary must cross-compile for each of these without cgo.
var targets = []string{
	"linux/amd64", "linux/arm64",
	"darwin/amd64", "darwin/arm64",
	"windows/amd64", "windows/arm64",
}

func main() {
	if err := chdirRoot(); err != nil {
		fail(err)
	}
	if len(os.Args) < 2 {
		usage()
		return
	}
	t, ok := tasks[os.Args[1]]
	if !ok {
		usage()
		fail(fmt.Errorf("unknown task %q", os.Args[1]))
	}
	if err := t.run(os.Args[2:]); err != nil {
		fail(err)
	}
}

func usage() {
	names := make([]string, 0, len(tasks))
	for n := range tasks {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Println("usage: go run ./dev <task> [args]")
	for _, n := range names {
		fmt.Printf("  %-13s %s\n", n, tasks[n].help)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "dev:", err)
	os.Exit(1)
}

// chdirRoot moves to the module root so tasks work from any subdirectory.
func chdirRoot() error {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return err
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return errors.New("not inside the lanternfolk module")
	}
	return os.Chdir(filepath.Dir(gomod))
}

func step(name string) { fmt.Printf("\n▸ %s\n", name) }

func run(env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func goCmd(args ...string) error { return run(nil, "go", args...) }

// ---- tasks ----

func fmtTask(args []string) error {
	if len(args) > 0 && args[0] == "-w" {
		return run(nil, "gofmt", "-w", ".")
	}
	out, err := exec.Command("gofmt", "-l", ".").Output()
	if err != nil {
		return err
	}
	var bad []string
	for _, f := range strings.Fields(string(out)) {
		if !strings.HasPrefix(filepath.ToSlash(f), "dist/") {
			bad = append(bad, f)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("gofmt needed (run `go run ./dev fmt -w`):\n  %s", strings.Join(bad, "\n  "))
	}
	return nil
}

// raceSupported: -race needs cgo and a C compiler, which Windows CI runners lack.
func raceSupported() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	out, err := exec.Command("go", "env", "CGO_ENABLED").Output()
	return err == nil && strings.TrimSpace(string(out)) == "1"
}

func testTask(args []string) error {
	a := []string{"test", "-count=1"}
	race := raceSupported()
	for _, x := range args {
		if x == "-norace" {
			race = false
			continue
		}
		if x != "-race" {
			a = append(a, x)
		}
	}
	if race {
		a = append(a, "-race")
	}
	return goCmd(append(a, "./...")...)
}

func lintTask([]string) error {
	if err := ensureTools(); err != nil {
		return err
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		fmt.Println("  staticcheck GOOS=" + goos)
		if err := run([]string{"GOOS=" + goos}, toolPath("staticcheck"), "./..."); err != nil {
			return err
		}
	}
	return nil
}

func buildTask([]string) error {
	for _, t := range targets {
		goos, goarch, _ := strings.Cut(t, "/")
		fmt.Printf("  %-14s ", t)
		err := run([]string{"CGO_ENABLED=0", "GOOS=" + goos, "GOARCH=" + goarch},
			"go", "build", "-o", os.DevNull, "./cmd/...")
		if err != nil {
			return err
		}
		fmt.Println("ok")
	}
	return nil
}

func ciTask([]string) error {
	steps := []struct {
		name string
		fn   func([]string) error
	}{
		{"fmt", fmtTask},
		{"vet", tasks["vet"].run},
		{"lint", lintTask},
		{"test", testTask},
		{"build (CGO_ENABLED=0, all targets)", buildTask},
		{"vuln", tasks["vuln"].run},
	}
	for _, s := range steps {
		step(s.name)
		if err := s.fn(nil); err != nil {
			return err
		}
	}
	fmt.Println("\n✓ ci passed")
	return nil
}

func snapshotTask([]string) error {
	return tool("goreleaser", "release", "--snapshot", "--clean", "--skip=publish,ko")
}

func releaseTask([]string) error {
	if os.Getenv("GITHUB_TOKEN") == "" {
		return errors.New("GITHUB_TOKEN is not set; releases run in CI on a v* tag")
	}
	return tool("goreleaser", "release", "--clean")
}

// The Asuka app: desktop (mac, windows, linux), mobile (ios, android), web.
const appDir = "cmd/lanternfolk-app"

// asukaTask runs the asuka CLI on the app: `go run ./dev asuka build -p ios
// --simulator` runs `asuka build -p ios --simulator cmd/lanternfolk-app`.
// Desktop and web builds need only Go. iOS needs Xcode, and Android needs a
// JDK, the Android SDK, and the NDK (asuka's Shipping guide).
func asukaTask(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: go run ./dev asuka <build|publish|dev|vet|...> [flags]")
	}
	if err := ensureAsuka(); err != nil {
		return err
	}
	// `dev` and `build` take the app's directory as their last argument.
	switch args[0] {
	case "build", "publish", "dev":
		args = append(args, appDir)
	}
	return run(nil, toolPath("asuka"), args...)
}

// desktopDistTask builds the desktop app with asuka and archives it as
// dist/desktop/lanternfolk-app_<version>_<os>_<arch>.{zip,tar.gz}.
// asuka builds macOS apps only on a Mac; Windows and Linux build anywhere,
// so CI makes those on a Linux runner and only the Mac on a macOS runner.
//
//	VERSION=v1.2.3 go run ./dev desktop-dist -platforms windows,linux
//
// MAC_SIGN_IDENTITY ("Developer ID Application: …"), with ASUKA_APPLE_ID,
// ASUKA_APPLE_PASSWORD, and ASUKA_TEAM_ID set, signs and notarizes the Mac
// app. ASUKA_WINDOWS_CERT and ASUKA_WINDOWS_CERT_PASSWORD sign the .exe
// (asuka reads those itself).
func desktopDistTask(args []string) error {
	platforms := map[string]bool{"windows": true, "linux": true, "mac": runtime.GOOS == "darwin"}
	for i := 0; i < len(args); i++ {
		if args[i] == "-platforms" && i+1 < len(args) {
			platforms = map[string]bool{}
			for _, p := range strings.Split(args[i+1], ",") {
				platforms[strings.TrimSpace(p)] = true
			}
			i++
		}
	}
	if platforms["mac"] && runtime.GOOS != "darwin" {
		return errors.New("asuka builds macOS apps only on a Mac")
	}
	if err := ensureAsuka(); err != nil {
		return err
	}
	// asuka sets its own -ldflags, so the version goes in through --version:
	// it is built into the app (Info.plist, the .exe resource, AppStream) and
	// read at runtime with sdk/update.Version().
	version := strings.TrimPrefix(os.Getenv("VERSION"), "v")
	if version == "" {
		version = "0.0.0-dev"
	}
	out, _ := filepath.Abs(filepath.Join("dist", "desktop"))
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	type job struct{ platform, arch string }
	var jobs []job
	for _, p := range []string{"windows", "linux"} {
		if platforms[p] {
			jobs = append(jobs, job{p, "amd64"}, job{p, "arm64"})
		}
	}
	if platforms["mac"] {
		jobs = append(jobs, job{"mac", "universal"})
	}
	for _, j := range jobs {
		step(fmt.Sprintf("desktop %s/%s", j.platform, j.arch))
		build := filepath.Join(filepath.Dir(out), "desktop-build", j.platform+"-"+j.arch)
		if err := os.RemoveAll(build); err != nil {
			return err
		}
		a := []string{"build", "-p", j.platform, "--version", version, "-o", build}
		if j.platform == "mac" {
			if id := os.Getenv("MAC_SIGN_IDENTITY"); id != "" {
				a = append(a, "--sign", id, "--notarize", "env")
			}
		} else {
			a = append(a, "--arch", j.arch)
		}
		if err := run(nil, toolPath("asuka"), append(a, appDir)...); err != nil {
			return err
		}
		osName := j.platform
		if osName == "mac" {
			osName = "darwin"
		}
		name := filepath.Join(out, fmt.Sprintf("lanternfolk-app_%s_%s_%s", version, osName, j.arch))
		var err error
		switch j.platform {
		case "linux": // asuka already made the .tar.gz
			var src string
			if src, err = globOne(filepath.Join(build, "linux", "*.tar.gz"))(); err == nil {
				err = copyFile(src, name+".tar.gz")
			}
		case "windows":
			err = zipDir(filepath.Join(build, "Lanternfolk"), name+".zip")
		case "mac":
			err = zipDir(filepath.Join(build, "Lanternfolk.app"), name+".zip")
		}
		if err != nil {
			return err
		}
	}
	fmt.Println("\n✓ desktop packages in", out)
	return nil
}

// ensureAsuka installs the asuka CLI at the version of the asuka module in
// go.mod. The CLI carries the iOS and Android hosts, so it must match the
// library the app links (asuka's Shipping guide, "In CI").
func ensureAsuka() error {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}", "github.com/egoist/asuka").Output()
	if err != nil {
		return fmt.Errorf("go list -m github.com/egoist/asuka: %w", err)
	}
	version := strings.TrimSpace(string(out))
	stamp := filepath.Join("bin", "tools", ".asuka-version")
	if b, err := os.ReadFile(stamp); err == nil && string(b) == version {
		if _, err := os.Stat(toolPath("asuka")); err == nil {
			return nil
		}
	}
	fmt.Printf("  installing asuka %s into bin/tools (matches go.mod)…\n", version)
	gobin, _ := filepath.Abs(filepath.Join("bin", "tools"))
	if err := os.MkdirAll(gobin, 0o755); err != nil {
		return err
	}
	if err := run([]string{"GOBIN=" + gobin, "CGO_ENABLED=0"},
		"go", "install", "github.com/egoist/asuka/cmd/asuka@"+version); err != nil {
		return err
	}
	return os.WriteFile(stamp, []byte(version), 0o644)
}

func constPath(p string) func() (string, error) { return func() (string, error) { return p, nil } }

func globOne(pattern string) func() (string, error) {
	return func() (string, error) {
		m, err := filepath.Glob(pattern)
		if err != nil || len(m) != 1 {
			return "", fmt.Errorf("expected one file matching %s, got %v (%v)", pattern, m, err)
		}
		return m[0], nil
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// zipDir zips dir (keeping dir's own name as the top folder), preserving
// file modes and symlinks so a macOS .app stays runnable.
func zipDir(dir, dst string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	parent := filepath.Dir(dir)
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(parent, path)
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
			_, err = zw.CreateHeader(hdr)
			return err
		}
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, err = w.Write([]byte(target))
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(w, src)
		return err
	})
	if err := errors.Join(walkErr, zw.Close()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ---- pinned tools ----

var toolPkgs = map[string]string{
	"staticcheck": "honnef.co/go/tools/cmd/staticcheck",
	"govulncheck": "golang.org/x/vuln/cmd/govulncheck",
	"goreleaser":  "github.com/goreleaser/goreleaser/v2",
}

func toolPath(name string) string {
	p := filepath.Join("bin", "tools", name)
	if runtime.GOOS == "windows" {
		p += ".exe"
	}
	abs, _ := filepath.Abs(p)
	return abs
}

func tool(name string, args ...string) error {
	if err := ensureTools(); err != nil {
		return err
	}
	return run(nil, toolPath(name), args...)
}

// ensureTools builds bin/tools/* when tools/go.mod or go.sum changed.
func ensureTools() error {
	sum, err := toolsHash()
	if err != nil {
		return err
	}
	stamp := filepath.Join("bin", "tools", ".stamp")
	if b, err := os.ReadFile(stamp); err == nil && string(b) == sum {
		missing := false
		for n := range toolPkgs {
			if _, err := os.Stat(toolPath(n)); err != nil {
				missing = true
			}
		}
		if !missing {
			return nil
		}
	}
	fmt.Println("  building pinned tools into bin/tools (first run or tools/go.mod changed)…")
	pkgs := make([]string, 0, len(toolPkgs))
	for _, p := range toolPkgs {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	out, _ := filepath.Abs(filepath.Join("bin", "tools"))
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	args := append([]string{"-C", "tools", "build", "-o", out + string(os.PathSeparator)}, pkgs...)
	if err := run([]string{"CGO_ENABLED=0"}, "go", args...); err != nil {
		return err
	}
	return os.WriteFile(stamp, []byte(sum), 0o644)
}

func toolsHash() (string, error) {
	h := sha256.New()
	for _, f := range []string{"tools/go.mod", "tools/go.sum"} {
		fh, err := os.Open(f)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(h, fh)
		fh.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
