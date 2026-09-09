package devtools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestJustfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command fixtures")
	}
	just, err := exec.LookPath("just")
	if err != nil {
		if os.Getenv("REQUIRE_JUST") == "1" {
			t.Fatal(err)
		}
		t.Skip("install just to run recipe contracts")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
		fail bool
	}{
		{"default", nil, "", false},
		{"test arguments", []string{"test", "-run=Test name; $(echo unsafe)"}, "test\n-race\n-run=Test name; $(echo unsafe)\n./...\n", false},
		{"examples", []string{"test-examples"}, "test\n-race\n./...\n", false},
		{"bdd", []string{"bdd", "--tags=@smoke and not @slow"}, "run\n./cmd/godog\n-f\nprogress\n--strict\n--tags=@smoke and not @slow\n", false},
		{"cli", []string{"godog", "workflow", "plan", "a file.yaml"}, "run\n./cmd/godog\nworkflow\nplan\na file.yaml\n", false},
		{"failure", []string{"test"}, "test\n-race\n./...\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			log := filepath.Join(tmp, "calls")
			script := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\" >> \"$CALL_LOG\"\nexit \"${GO_EXIT:-0}\"\n"
			if err := os.WriteFile(filepath.Join(tmp, "go"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(just, tc.args...)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+tmp+string(os.PathListSeparator)+os.Getenv("PATH"), "CALL_LOG="+log, "GO_EXIT=0")
			if tc.fail {
				cmd.Env = append(cmd.Env, "GO_EXIT=7")
			}
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v output=%s", err, out)
			}
			data, _ := os.ReadFile(log)
			if tc.want == "" {
				if len(data) != 0 || !strings.Contains(string(out), "Available recipes") {
					t.Fatalf("default must show help only: %s / %s", out, data)
				}
				return
			}
			dir := root
			if tc.name == "examples" {
				dir = filepath.Join(root, "_examples")
			}
			if string(data) != dir+"\n"+tc.want {
				t.Fatalf("got %q want %q", data, dir+"\n"+tc.want)
			}
		})
	}
}

func TestJustfileFormattingAndCheckFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command fixtures")
	}
	just, err := exec.LookPath("just")
	if err != nil {
		if os.Getenv("REQUIRE_JUST") == "1" {
			t.Fatal(err)
		}
		t.Skip("install just to run recipe contracts")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		recipe     string
		formatExit string
		formatText string
		goExit     string
		want       string
		fail       bool
	}{
		{"clean formatting", "fmt-check", "0", "", "0", "gofmt\n-l\n.\n", false},
		{"unformatted source", "fmt-check", "0", "unformatted.go", "0", "gofmt\n-l\n.\n", true},
		{"formatter error without stdout", "fmt-check", "7", "", "0", "gofmt\n-l\n.\n", true},
		{"check stops on formatter error", "check", "7", "", "0", "gofmt\n-l\n.\n", true},
		{"check stops on vet error", "check", "0", "", "7", "gofmt\n-l\n.\ngo\nvet\n./...\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			log := filepath.Join(tmp, "calls")
			fixtures := map[string]string{
				"go":    "#!/bin/sh\nprintf '%s\\n' go \"$@\" >> \"$CALL_LOG\"\nexit \"$GO_EXIT\"\n",
				"gofmt": "#!/bin/sh\nprintf '%s\\n' gofmt \"$@\" >> \"$CALL_LOG\"\nprintf '%s' \"$FORMAT_TEXT\"\nexit \"$FORMAT_EXIT\"\n",
			}
			for name, script := range fixtures {
				if err := os.WriteFile(filepath.Join(tmp, name), []byte(script), 0755); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(just, tc.recipe)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+tmp+string(os.PathListSeparator)+os.Getenv("PATH"), "CALL_LOG="+log,
				"GO_EXIT="+tc.goExit, "FORMAT_EXIT="+tc.formatExit, "FORMAT_TEXT="+tc.formatText)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail {
				t.Errorf("error=%v output=%s", err, out)
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tc.want {
				t.Errorf("got commands %q want %q", data, tc.want)
			}
			if tc.formatText != "" && !strings.Contains(string(out), tc.formatText) {
				t.Errorf("missing unformatted source path in output: %s", out)
			}
		})
	}
}
