package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, configFileName)
	content := `[wrapper]
force_password_auth = false

[servers."12.11061"]
host = "10.0.255.12"
port = 11061
user = "app"
password = "secret"

[servers."12.11061".options]
StrictHostKeyChecking = "accept-new"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := cfg.Servers["12.11061"]
	if entry.hostName() != "10.0.255.12" || entry.Port != 11061 || entry.Password != "secret" {
		t.Fatalf("unexpected server: %#v", entry)
	}
	if cfg.forcePasswordAuth() {
		t.Fatal("force_password_auth should be false")
	}
}

func TestLoadConfigParentTableWithInlineServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), configFileName)
	content := `[servers]
"5.13001" = { host = "10.0.255.5", port = 13001, user = "app", password = "secret-5" }
"12.11061" = { host = "10.0.255.12", port = 11061, user = "app", password = "secret-12" }
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 2 || cfg.Servers["5.13001"].Port != 13001 || cfg.Servers["12.11061"].Password != "secret-12" {
		t.Fatalf("unexpected inline servers: %#v", cfg.Servers)
	}
}

func TestLoadConfigRejectsMissingPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), configFileName)
	content := `[servers.test]
host = "example.test"
port = 22
user = "app"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "missing password") {
		t.Fatalf("error = %v, want missing password", err)
	}
}

func TestLoadConfigRejectsPasswordOption(t *testing.T) {
	path := filepath.Join(t.TempDir(), configFileName)
	content := `[servers.test]
host = "example.test"
port = 22
user = "app"
password = "secret"

[servers.test.options]
Password = "must-not-be-generated"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "structured server field") {
		t.Fatalf("error = %v, want structured server field", err)
	}
}

func TestSSHDestination(t *testing.T) {
	tests := []struct {
		args []string
		want string
		ok   bool
	}{
		{[]string{"-V"}, "", false},
		{[]string{"-G", "12.11061"}, "12.11061", true},
		{[]string{"-vvv", "-p", "11061", "app@12.11061", "echo", "ok"}, "app@12.11061", true},
		{[]string{"-vp", "11061", "12.11061"}, "12.11061", true},
		{[]string{"-vFcustom.conf", "12.11061"}, "12.11061", true},
		{[]string{"-o", "ConnectTimeout=10", "-D61355", "12.11061", "bash"}, "12.11061", true},
		{[]string{"--", "host"}, "host", true},
		{[]string{"-Q", "cipher"}, "", false},
	}
	for _, test := range tests {
		got, ok := sshDestination(test.args)
		if got != test.want || ok != test.ok {
			t.Errorf("sshDestination(%q) = (%q, %v), want (%q, %v)", test.args, got, ok, test.want, test.ok)
		}
	}
}

func TestInvocationClient(t *testing.T) {
	for executable, want := range map[string]clientKind{
		"ssh-wrapper":     clientSSH,
		"ssh-wrapper.exe": clientSSH,
		"scp-wrapper":     clientSCP,
		"scp-wrapper.exe": clientSCP,
	} {
		if got := invocationClient(executable); got != want {
			t.Errorf("invocationClient(%q) = %q, want %q", executable, got, want)
		}
	}
}

func TestHasRemoteTarget(t *testing.T) {
	if hasRemoteTarget(clientSSH, []string{"-V"}) {
		t.Fatal("ssh -V must not be treated as a remote invocation")
	}
	if runtime.GOOS == "windows" && hasRemoteTarget(clientSCP, []string{`C:\tmp\a`, `D:\tmp\b`}) {
		t.Fatal("local Windows paths must not be treated as remote SCP operands on Windows")
	}
	if !hasRemoteTarget(clientSCP, []string{"local", "12.11061:/tmp"}) {
		t.Fatal("remote SCP operand was not detected")
	}
}

func TestSCPRemoteAliases(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"upload", []string{"local.txt", "12.11061:/tmp/"}, []string{"12.11061"}},
		{"download", []string{"app@12.11061:/tmp/a", "."}, []string{"12.11061"}},
		{"URI", []string{"scp://app@12.11061:11061/tmp/a", "."}, []string{"12.11061"}},
		{"IPv6", []string{"[2001:db8::1]:/tmp/a", "."}, []string{"2001:db8::1"}},
		{"IPv6 with user", []string{"app@[2001:db8::1]:/tmp/a", "."}, []string{"2001:db8::1"}},
		{"options", []string{"-P", "2200", "-rq", "local", "12.11061:/tmp"}, []string{"12.11061"}},
		{"combined option with value", []string{"-vP", "2200", "local", "12.11061:/tmp"}, []string{"12.11061"}},
		{"combined attached value", []string{"-vP2200", "local", "12.11061:/tmp"}, []string{"12.11061"}},
		{"Unix path with colon", []string{"./archive:old.zip", "/tmp/result:old.zip"}, nil},
		{"same remote", []string{"12.11061:/a", "12.11061:/b"}, []string{"12.11061"}},
		{"different remotes", []string{"12.11061:/a", "5.13001:/b"}, []string{"12.11061", "5.13001"}},
	}
	if runtime.GOOS == "windows" {
		tests = append(tests, struct {
			name string
			args []string
			want []string
		}{"Windows path", []string{`C:\tmp\a.txt`, `D:\out\a.txt`, `E:relative.txt`}, nil})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := scpRemoteAliases(test.args)
			if strings.Join(got, ",") != strings.Join(test.want, ",") {
				t.Fatalf("scpRemoteAliases(%q) = %q, want %q", test.args, got, test.want)
			}
		})
	}
}

func TestConfiguredAliasForSCP(t *testing.T) {
	servers := map[string]server{"12.11061": {}, "5.13001": {}}
	alias, configured, err := configuredAlias(clientSCP, []string{"local", "12.11061:/tmp"}, servers)
	if err != nil || !configured || alias != "12.11061" {
		t.Fatalf("configuredAlias = (%q, %v, %v)", alias, configured, err)
	}
	_, _, err = configuredAlias(clientSCP, []string{"12.11061:/a", "5.13001:/b"}, servers)
	if err == nil || !strings.Contains(err.Error(), "different remote hosts") {
		t.Fatalf("different remote error = %v", err)
	}
	_, _, err = configuredAlias(clientSCP, []string{"12.11061:/a", "unconfigured:/b"}, servers)
	if err == nil || !strings.Contains(err.Error(), "different remote hosts") {
		t.Fatalf("configured-to-unconfigured remote error = %v", err)
	}
}

func TestRunClientSCPInjectsArgumentsAndAskpassEnvironment(t *testing.T) {
	realClient, err := exec.LookPath("go")
	if err != nil {
		t.Skip("Go executable is unavailable")
	}
	path := filepath.Join(t.TempDir(), configFileName)
	content := fmt.Sprintf(`[wrapper]
real_scp = %q

[servers.test]
host = "example.test"
port = 2222
user = "app"
password = "secret"
`, filepath.ToSlash(realClient))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(configEnvName, path)
	t.Setenv(modeEnvName, "")

	originalNewCommand := newCommand
	defer func() { newCommand = originalNewCommand }()
	var capturedName string
	var capturedArgs []string
	var created *exec.Cmd
	newCommand = func(name string, args ...string) *exec.Cmd {
		capturedName = name
		capturedArgs = append([]string(nil), args...)
		created = exec.Command(os.Args[0], "-test.run=^TestClientProcessHelper$")
		return created
	}

	var stdout, stderr bytes.Buffer
	code := runClient(clientSCP, []string{"local.txt", "test:/tmp/"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runClient code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.EqualFold(filepath.Clean(capturedName), filepath.Clean(realClient)) {
		t.Fatalf("client = %q, want %q", capturedName, realClient)
	}
	joined := strings.Join(capturedArgs, " ")
	for _, expected := range []string{"HostName=example.test", "Port=2222", "User=app", "local.txt test:/tmp/"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("SCP args %q do not contain %q", capturedArgs, expected)
		}
	}
	for _, expected := range []string{"SSH_ASKPASS=", modeEnvName + "=1", aliasEnvName + "=test"} {
		if !environmentContains(created.Env, expected) {
			t.Errorf("SCP environment does not contain %q", expected)
		}
	}
}

func TestClientProcessHelper(t *testing.T) {}

func environmentContains(env []string, expected string) bool {
	for _, item := range env {
		if strings.HasPrefix(item, expected) {
			return true
		}
	}
	return false
}

func TestConfiguredSSHArgs(t *testing.T) {
	entry := server{Host: "10.0.255.12", Port: 11061, User: "app"}
	got := configuredSSHArgs([]string{"-T", "12.11061"}, entry, true)
	joined := strings.Join(got, " ")
	for _, expected := range []string{"HostName=10.0.255.12", "Port=11061", "User=app", "PubkeyAuthentication=no"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("configured args %q do not contain %q", got, expected)
		}
	}
	if got[0] != "-T" || got[len(got)-1] != "12.11061" {
		t.Fatalf("SSH option or destination moved incorrectly: %q", got)
	}
}

func TestConfiguredSSHArgsKeepsExplicitOptionsFirstAndCommandLast(t *testing.T) {
	entry := server{Host: "10.0.255.12", Port: 11061, User: "app"}
	got := configuredSSHArgs([]string{"-p", "2200", "-lroot", "12.11061", "echo", "-n"}, entry, false)
	joined := strings.Join(got, " ")
	if !strings.HasPrefix(joined, "-p 2200 -lroot -o HostName=10.0.255.12") {
		t.Fatalf("explicit options did not remain before defaults: %q", got)
	}
	if !strings.HasSuffix(joined, "12.11061 echo -n") {
		t.Fatalf("destination or remote command moved: %q", got)
	}
}

func TestConfiguredSSHArgsInsertsOptionsBeforeDoubleDash(t *testing.T) {
	entry := server{Host: "10.0.255.12", Port: 11061, User: "app"}
	got := configuredSSHArgs([]string{"--", "12.11061", "echo"}, entry, false)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "User=app -- 12.11061 echo") {
		t.Fatalf("SSH defaults were not inserted before --: %q", got)
	}
}

func TestConfiguredSCPArgsKeepsExplicitOptionsBeforeDefaults(t *testing.T) {
	entry := server{Host: "10.0.255.12", Port: 11061, User: "app"}
	got := configuredSCPArgs([]string{"-P", "2200", "local.txt", "12.11061:/tmp"}, entry, false)
	joined := strings.Join(got, " ")
	if !strings.HasPrefix(joined, "-P 2200 -o HostName=10.0.255.12") {
		t.Fatalf("explicit SCP options did not remain before defaults: %q", got)
	}
	if !strings.HasSuffix(joined, "local.txt 12.11061:/tmp") {
		t.Fatalf("SCP operands moved: %q", got)
	}
}

func TestConfiguredSCPArgsInsertsOptionsBeforeDoubleDash(t *testing.T) {
	entry := server{Host: "10.0.255.12", Port: 11061, User: "app"}
	got := configuredSCPArgs([]string{"--", "local.txt", "12.11061:/tmp"}, entry, false)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "User=app -- local.txt 12.11061:/tmp") {
		t.Fatalf("SCP defaults were not inserted before --: %q", got)
	}
}

func TestConfiguredSCPArgsPreservesOperandUserAndPort(t *testing.T) {
	entry := server{Host: "10.0.255.12", Port: 11061, User: "app"}
	got := configuredSCPArgs([]string{"scp://other@12.11061:2200/tmp/a", "."}, entry, false)
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "User=app") || strings.Contains(joined, "Port=11061") {
		t.Fatalf("TOML user or port overrides explicit SCP URI: %q", got)
	}
	if !strings.Contains(joined, "HostName=10.0.255.12") {
		t.Fatalf("configured HostName is missing: %q", got)
	}

	got = configuredSCPArgs([]string{"other@12.11061:/tmp/a", "."}, entry, false)
	joined = strings.Join(got, " ")
	if strings.Contains(joined, "User=app") {
		t.Fatalf("TOML user overrides explicit SCP user: %q", got)
	}
	if !strings.Contains(joined, "Port=11061") {
		t.Fatalf("configured port is missing: %q", got)
	}
}

func TestRenderSSHConfigIsSortedAndOmitsPasswords(t *testing.T) {
	servers := map[string]server{
		"z": {Host: "z.example", Port: 2200, User: "root", Password: "top-secret"},
		"a": {Host: "a.example", Port: 22, User: "app", Password: "other-secret"},
	}
	got := renderSSHConfig(servers)
	if strings.Index(got, "Host a") > strings.Index(got, "Host z") {
		t.Fatalf("hosts are not sorted:\n%s", got)
	}
	if strings.Contains(got, "top-secret") || strings.Contains(got, "other-secret") {
		t.Fatalf("generated config exposes a password:\n%s", got)
	}
}

func TestRunAskpassSelectsPasswordByAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), configFileName)
	content := `[servers.test]
host = "example.test"
port = 22
user = "app"
password = "selected-secret"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(configEnvName, path)
	t.Setenv(aliasEnvName, "test")

	var stdout, stderr bytes.Buffer
	if code := runAskpass([]string{"app@example.test's password:"}, &stdout, &stderr); code != 0 {
		t.Fatalf("runAskpass code = %d, stderr = %q", code, stderr.String())
	}
	if got := stdout.String(); got != "selected-secret\n" {
		t.Fatalf("askpass output = %q", got)
	}
}

func TestRunAskpassRejectsHostKeyConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), configFileName)
	content := `[servers.test]
host = "example.test"
port = 22
user = "app"
password = "selected-secret"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(configEnvName, path)
	t.Setenv(aliasEnvName, "test")

	var stdout, stderr bytes.Buffer
	prompt := "Are you sure you want to continue connecting (yes/no/[fingerprint])?"
	if code := runAskpass([]string{prompt}, &stdout, &stderr); code != 0 {
		t.Fatalf("runAskpass code = %d, stderr = %q", code, stderr.String())
	}
	if got := stdout.String(); got != "no\n" {
		t.Fatalf("askpass confirmation output = %q", got)
	}
}

func TestSetEnvReplacesExistingValue(t *testing.T) {
	key := "SSH_ASKPASS"
	existingKey := key
	if runtime.GOOS == "windows" {
		existingKey = "ssh_askpass"
	}
	got := setEnv([]string{"A=1", existingKey + "=old"}, key, "new")
	if strings.Join(got, ";") != "A=1;SSH_ASKPASS=new" {
		t.Fatalf("environment = %q", got)
	}
}

func TestContainsConfigLine(t *testing.T) {
	content := "Host foo\r\n  HostName example.test\r\nInclude C:/tools/ssh-wrapper.hosts.conf\r\n"
	if !containsConfigLine(content, "Include C:/tools/ssh-wrapper.hosts.conf") {
		t.Fatal("include line was not found")
	}
}

func TestInstallSSHConfigIsIdempotentAndOmitsPasswords(t *testing.T) {
	configDir := t.TempDir()
	homeDir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", homeDir)
	} else {
		t.Setenv("HOME", homeDir)
	}
	sshDir := filepath.Join(homeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sshConfigPath := filepath.Join(sshDir, "config")
	original := "Host existing\n  HostName existing.example\n"
	if err := os.WriteFile(sshConfigPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config{
		path: filepath.Join(configDir, configFileName),
		Servers: map[string]server{
			"test": {Host: "test.example", Port: 2222, User: "app", Password: "must-not-leak"},
		},
	}
	generatedPath, installedConfigPath, err := installSSHConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if installedConfigPath != sshConfigPath {
		t.Fatalf("installed path = %q, want %q", installedConfigPath, sshConfigPath)
	}
	if _, _, err := installSSHConfig(cfg); err != nil {
		t.Fatalf("second install: %v", err)
	}

	generated, err := os.ReadFile(generatedPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(generated), "must-not-leak") {
		t.Fatalf("generated config contains password: %s", generated)
	}
	installed, err := os.ReadFile(sshConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	includeLine := "Include " + quoteSSHValue(filepath.ToSlash(generatedPath))
	if strings.Count(string(installed), includeLine) != 1 {
		t.Fatalf("installed config must contain one Include line:\n%s", installed)
	}
	if !strings.Contains(string(installed), original) {
		t.Fatalf("existing SSH config was not preserved:\n%s", installed)
	}
}

func TestConfirmationPrompt(t *testing.T) {
	if !isConfirmationPrompt("Are you sure you want to continue connecting (yes/no/[fingerprint])?") {
		t.Fatal("host key confirmation prompt was not recognized")
	}
	if isConfirmationPrompt("app@example's password:") {
		t.Fatal("password prompt was treated as confirmation")
	}
}
