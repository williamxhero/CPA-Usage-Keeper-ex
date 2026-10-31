package test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type channelWorkflow struct {
	Jobs map[string]struct {
		Uses  string    `yaml:"uses"`
		Needs yaml.Node `yaml:"needs"`
		Steps []struct {
			Name string            `yaml:"name"`
			Uses string            `yaml:"uses"`
			Run  string            `yaml:"run"`
			With map[string]string `yaml:"with"`
			Env  map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func readChannelWorkflow(t *testing.T, name string) channelWorkflow {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var workflow channelWorkflow
	if err := yaml.Unmarshal(content, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}

func channelScript(t *testing.T, workflow, job, stepName string) string {
	t.Helper()
	for _, step := range readChannelWorkflow(t, workflow).Jobs[job].Steps {
		if step.Name == stepName && step.Run != "" {
			return strings.ReplaceAll(step.Run, "\r\n", "\n")
		}
	}
	t.Fatalf("missing %s/%s/%s", workflow, job, stepName)
	return ""
}

func TestReleaseChannelPolicy(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is required for the release runner")
	}
	script := channelScript(t, "release-policy.yml", "validate", "Validate release tag and source branch")
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	repo := filepath.Join(root, "checkout")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "--bare", origin)
	git("init", "-b", "main", repo)
	git("-C", repo, "config", "user.name", "Release Test")
	git("-C", repo, "config", "user.email", "release-test@example.invalid")
	git("-C", repo, "config", "commit.gpgsign", "false")
	git("-C", repo, "config", "tag.gpgsign", "false")
	commit := func(message string) string {
		git("-C", repo, "commit", "--allow-empty", "-m", message)
		return git("-C", repo, "rev-parse", "HEAD")
	}
	shared := commit("shared history")
	git("-C", repo, "checkout", "-b", "release/v1")
	v1 := commit("v1 maintenance")
	git("-C", repo, "checkout", "main")
	main := commit("current stable")
	git("-C", repo, "checkout", "-b", "next")
	next := commit("unreleased development")
	git("-C", repo, "remote", "add", "origin", origin)
	git("-C", repo, "push", "origin", "main", "release/v1", "next")

	for _, tc := range []struct {
		name, tag, commit, event, head, ref, missingBranch string
		annotated, pass                                    bool
		latest, formula                                    string
	}{
		{name: "v1", tag: "v1.15.12", commit: v1, pass: true, latest: "false", formula: "cpa-usage-keeper@1"},
		{name: "v2 annotated", tag: "v2.0.0", commit: main, annotated: true, pass: true, latest: "true", formula: "cpa-usage-keeper"},
		{name: "v3", tag: "v3.0.0", commit: main, pass: true, latest: "true", formula: "cpa-usage-keeper"},
		{name: "v4", tag: "v4.0.0", commit: main, pass: true, latest: "true", formula: "cpa-usage-keeper"},
		{name: "older commit on v1", tag: "v1.15.10", commit: shared, pass: true, latest: "false", formula: "cpa-usage-keeper@1"},
		{name: "v1 on main", tag: "v1.15.13", commit: main},
		{name: "v1 on next", tag: "v1.15.14", commit: next},
		{name: "v2 on v1", tag: "v2.0.1", commit: v1},
		{name: "v2 on next", tag: "v2.0.2", commit: next},
		{name: "missing v1 branch despite cached ref", tag: "v1.15.15", commit: v1, missingBranch: "release/v1"},
		{name: "missing main branch", tag: "v2.0.3", commit: main, missingBranch: "main"},
		{name: "moved tag", tag: "v2.0.4", commit: main, event: next},
		{name: "wrong checkout", tag: "v2.0.5", commit: main, head: next},
		{name: "branch event", tag: "v2.0.6", commit: main, ref: "refs/heads/main"},
		{name: "incomplete version", tag: "v2.0", commit: main},
		{name: "prerelease is not stable", tag: "v2.0.0-rc.1", commit: main},
		{name: "leading zero", tag: "v01.0.0", commit: main},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.annotated {
				git("-C", repo, "tag", "-a", tc.tag, tc.commit, "-m", tc.tag)
			} else {
				git("-C", repo, "tag", tc.tag, tc.commit)
			}
			if tc.missingBranch != "" {
				git("--git-dir", origin, "update-ref", "-d", "refs/heads/"+tc.missingBranch)
				defer git("-C", repo, "push", "origin", tc.missingBranch)
			}
			head, event, ref := tc.head, tc.event, tc.ref
			if head == "" {
				head = tc.commit
			}
			if event == "" {
				event = tc.commit
			}
			if ref == "" {
				ref = "refs/tags/" + tc.tag
			}
			git("-C", repo, "checkout", "--detach", head)
			outputPath := filepath.Join(t.TempDir(), "output")
			cmd := exec.Command(bash, "-e", "-c", script)
			cmd.Dir = repo
			cmd.Env = append(os.Environ(), "GITHUB_REF_NAME="+tc.tag, "GITHUB_REF="+ref,
				"GITHUB_SHA="+event, "GITHUB_OUTPUT="+filepath.ToSlash(outputPath),
				"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.pass {
				t.Fatalf("want pass=%t: %v\n%s", tc.pass, err, output)
			}
			values, readErr := os.ReadFile(outputPath)
			if tc.pass {
				want := "latest=" + tc.latest + "\nformula=" + tc.formula + "\n"
				if readErr != nil || string(values) != want {
					t.Fatalf("outputs=%q, err=%v; want %q", values, readErr, want)
				}
			} else if len(values) != 0 {
				t.Fatalf("failed validation emitted publishing outputs: %s", values)
			}
		})
	}
}

func TestReleasePublishersUseValidatedChannels(t *testing.T) {
	for _, file := range []string{"docker-publish.yml", "binary-release.yml"} {
		workflow := readChannelWorkflow(t, file)
		if workflow.Jobs["release-policy"].Uses != "./.github/workflows/release-policy.yml" {
			t.Fatalf("%s must call the shared release policy", file)
		}
		for id, job := range workflow.Jobs {
			if id == "release-policy" {
				continue
			}
			var dependencies []string
			if job.Needs.Kind == yaml.ScalarNode {
				dependencies = []string{job.Needs.Value}
			} else if err := job.Needs.Decode(&dependencies); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains("|"+strings.Join(dependencies, "|")+"|", "|release-policy|") {
				t.Errorf("%s/%s can bypass the release policy", file, id)
			}
			for _, step := range job.Steps {
				switch step.Uses {
				case "docker/metadata-action@v6":
					if step.With["flavor"] != "latest=false" || !strings.Contains(step.With["tags"], "type=raw,value=latest,enable=${{ needs.release-policy.outputs.latest == 'true' }}") {
						t.Error("Docker latest must be controlled exclusively by the release policy")
					}
				case "softprops/action-gh-release@v3":
					if step.With["make_latest"] != "${{ needs.release-policy.outputs.latest }}" {
						t.Error("GitHub Latest must follow the release policy")
					}
				}
				if step.Name == "Update formula" || step.Name == "Commit and push tap update" {
					if step.Env["FORMULA"] != "${{ needs.release-policy.outputs.formula }}" {
						t.Errorf("%s must use the selected formula", step.Name)
					}
				}
			}
		}
	}
}

func TestHomebrewReleaseUpdatePreservesFormulaSettings(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is required for the release runner")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the Linux tap updater")
	}
	script := channelScript(t, "binary-release.yml", "update-homebrew-tap", "Update formula")
	for _, tc := range []struct {
		name, version, formula string
		badSHA, missing        bool
		pass                   bool
	}{
		{name: "v1", version: "1.15.12", formula: "cpa-usage-keeper@1", pass: true},
		{name: "v2", version: "2.0.0", formula: "cpa-usage-keeper", pass: true},
		{name: "v3", version: "3.0.0", formula: "cpa-usage-keeper", pass: true},
		{name: "v4", version: "4.0.0", formula: "cpa-usage-keeper", pass: true},
		{name: "v1 cannot overwrite default", version: "1.15.12", formula: "cpa-usage-keeper"},
		{name: "v2 cannot overwrite v1", version: "2.0.0", formula: "cpa-usage-keeper@1"},
		{name: "missing formula", version: "1.15.12", formula: "cpa-usage-keeper@1", missing: true},
		{name: "bad checksum", version: "1.15.12", formula: "cpa-usage-keeper@1", badSHA: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "homebrew-tap", "Formula")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			originals := map[string]string{}
			for _, formula := range []string{"cpa-usage-keeper", "cpa-usage-keeper@1"} {
				if tc.missing && formula == tc.formula {
					continue
				}
				content := "version \"1.15.11\"\n"
				for _, arch := range []string{"arm64", "amd64"} {
					content += fmt.Sprintf("  url \"https://github.com/Willxup/cpa-usage-keeper/releases/download/v1.15.11/cpa-usage-keeper_v1.15.11_darwin_%s.tar.gz\"\n  sha256 \"%s\"\n", arch, strings.Repeat("a", 64))
				}
				content += `  bin.install "cpa-usage-keeper"` + "\n"
				content += fmt.Sprintf("# Preserve installation and service configuration\nrun [opt_bin/\"cpa-usage-keeper\", \"--env\", etc/\"%s.env\"]\n", formula)
				originals[formula] = content
				if err := os.WriteFile(filepath.Join(dir, formula+".rb"), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			armSHA, amdSHA := strings.Repeat("b", 64), strings.Repeat("c", 64)
			if tc.badSHA {
				amdSHA = "invalid"
			}
			tag := "v" + tc.version
			cmd := exec.Command(bash, "-e", "-c", script)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "FORMULA="+tc.formula, "TAG="+tag, "VERSION="+tc.version,
				"ARM_FILE=cpa-usage-keeper_"+tag+"_darwin_arm64.tar.gz", "AMD_FILE=cpa-usage-keeper_"+tag+"_darwin_amd64.tar.gz",
				"ARM_SHA="+armSHA, "AMD_SHA="+amdSHA)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.pass {
				t.Fatalf("want pass=%t: %v\n%s", tc.pass, err, output)
			}
			for formula, original := range originals {
				content, err := os.ReadFile(filepath.Join(dir, formula+".rb"))
				if err != nil {
					t.Fatal(err)
				}
				want := original
				if tc.pass && formula == tc.formula {
					want = strings.ReplaceAll(want, "1.15.11", tc.version)
					want = strings.Replace(want, strings.Repeat("a", 64), armSHA, 1)
					want = strings.Replace(want, strings.Repeat("a", 64), amdSHA, 1)
				}
				if string(content) != want {
					t.Errorf("%s contains unintended changes\ngot:\n%s\nwant:\n%s", formula, content, want)
				}
			}
		})
	}
}

func TestReleaseVersionFileRemainsV2Only(t *testing.T) {
	script := channelScript(t, "binary-release.yml", "publish-release", "Generate release version file")
	for _, tag := range []string{"v1.15.12", "v2.0.0", "v2.1.3", "v2.0.0-rc.1", "v3.0.0", "v4.0.0"} {
		t.Run(tag, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "dist"), 0755); err != nil {
				t.Fatal(err)
			}
			outputPath := filepath.Join(root, "outputs")
			cmd := exec.Command("bash", "-e", "-c", script)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "RELEASE_TAG="+tag, "GITHUB_OUTPUT="+filepath.ToSlash(outputPath))
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("generate version: %v\n%s", err, output)
			}
			data, err := os.ReadFile(filepath.Join(root, "dist", "version.txt"))
			want := tag == "v2.0.0" || tag == "v2.1.3"
			if want {
				if err != nil || string(data) != tag+"\n" {
					t.Fatalf("version file=%q err=%v", data, err)
				}
				output, err := os.ReadFile(outputPath)
				if err != nil || string(output) != "path=dist/version.txt\n" {
					t.Fatalf("version asset output=%q err=%v", output, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("unexpected version file for %s: %v", tag, err)
			}
		})
	}
	workflow := readChannelWorkflow(t, "binary-release.yml")
	uploaded := false
	for _, step := range workflow.Jobs["publish-release"].Steps {
		if step.Uses == "softprops/action-gh-release@v3" && strings.Contains(step.With["files"], "${{ steps.version_file.outputs.path }}") {
			uploaded = true
		}
	}
	if !uploaded {
		t.Fatal("release must upload the generated v2 version asset")
	}
}
