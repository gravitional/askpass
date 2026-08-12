package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	configFileName    = "ssh-wrapper-config.toml"
	generatedFileName = "ssh-wrapper.hosts.conf"
	configEnvName     = "SSH_WRAPPER_CONFIG"
	modeEnvName       = "_SSH_WRAPPER_ASKPASS"
	aliasEnvName      = "_SSH_WRAPPER_ALIAS"
	version           = "ssh-wrapper 0.2.0"
)

type config struct {
	Wrapper wrapperConfig     `toml:"wrapper"`
	Servers map[string]server `toml:"servers"`
	path    string
}

type wrapperConfig struct {
	RealSSH           string `toml:"real_ssh"`
	RealSCP           string `toml:"real_scp"`
	ForcePasswordAuth *bool  `toml:"force_password_auth"`
}

type clientKind string

const (
	clientSSH clientKind = "ssh"
	clientSCP clientKind = "scp"
)

type server struct {
	Host     string            `toml:"host"`
	HostName string            `toml:"hostname"`
	Port     int               `toml:"port"`
	User     string            `toml:"user"`
	Password string            `toml:"password"`
	Options  map[string]string `toml:"options"`
}

var newCommand = exec.Command

func (s server) hostName() string {
	if s.HostName != "" {
		return s.HostName
	}
	return s.Host
}

func (c config) forcePasswordAuth() bool {
	return c.Wrapper.ForcePasswordAuth == nil || *c.Wrapper.ForcePasswordAuth
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runClient(invocationClient(os.Args[0]), args, stdout, stderr)
}

func runClient(client clientKind, args []string, stdout, stderr io.Writer) int {
	if os.Getenv(modeEnvName) == "1" {
		return runAskpass(args, stdout, stderr)
	}

	if len(args) > 0 && strings.HasPrefix(args[0], "--wrapper-") {
		return runWrapperCommand(args, stdout, stderr)
	}

	if !hasRemoteTarget(client, args) {
		realClient, err := findRealClient(client, "")
		if err != nil {
			fmt.Fprintf(stderr, "ssh-wrapper: %v\n", err)
			return 127
		}
		return executeClient(realClient, args, os.Environ(), stdout, stderr)
	}

	cfg, err := loadConfig(defaultConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		realClient, findErr := findRealClient(client, "")
		if findErr != nil {
			fmt.Fprintf(stderr, "ssh-wrapper: %v\n", findErr)
			return 127
		}
		return executeClient(realClient, args, os.Environ(), stdout, stderr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ssh-wrapper: %v\n", err)
		return 2
	}

	configuredClient := cfg.Wrapper.RealSSH
	if client == clientSCP {
		configuredClient = cfg.Wrapper.RealSCP
	}
	configuredClient = resolveFromConfig(cfg.path, configuredClient)
	realClient, err := findRealClient(client, configuredClient)
	if err != nil {
		fmt.Fprintf(stderr, "ssh-wrapper: %v\n", err)
		return 127
	}

	alias, ok, err := configuredAlias(client, args, cfg.Servers)
	if err != nil {
		fmt.Fprintf(stderr, "ssh-wrapper: %v\n", err)
		return 2
	}
	if !ok {
		return executeClient(realClient, args, os.Environ(), stdout, stderr)
	}
	entry, configured := cfg.Servers[alias]
	if !configured {
		return executeClient(realClient, args, os.Environ(), stdout, stderr)
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "ssh-wrapper: locate executable: %v\n", err)
		return 1
	}

	env := os.Environ()
	env = setEnv(env, "SSH_ASKPASS", self)
	env = setEnv(env, "SSH_ASKPASS_REQUIRE", "force")
	env = setEnv(env, modeEnvName, "1")
	env = setEnv(env, aliasEnvName, alias)
	env = setEnv(env, configEnvName, cfg.path)
	if !hasEnv(env, "DISPLAY") {
		env = setEnv(env, "DISPLAY", "ssh-wrapper")
	}

	clientArgs := configuredClientArgs(client, args, entry, cfg.forcePasswordAuth())
	return executeClient(realClient, clientArgs, env, stdout, stderr)
}

func hasRemoteTarget(client clientKind, args []string) bool {
	if client == clientSCP {
		return len(scpRemoteAliases(args)) > 0
	}
	_, ok := sshDestination(args)
	return ok
}

func invocationClient(executable string) clientKind {
	name := strings.ToLower(filepath.Base(executable))
	name = strings.TrimSuffix(name, filepath.Ext(name))
	if strings.Contains(name, "scp") {
		return clientSCP
	}
	return clientSSH
}

func runAskpass(args []string, stdout, stderr io.Writer) int {
	alias := os.Getenv(aliasEnvName)
	if alias == "" {
		fmt.Fprintln(stderr, "ssh-wrapper: askpass alias is missing")
		return 1
	}
	cfg, err := loadConfig(defaultConfigPath())
	if err != nil {
		fmt.Fprintf(stderr, "ssh-wrapper: askpass: %v\n", err)
		return 1
	}
	entry, ok := cfg.Servers[alias]
	if !ok {
		fmt.Fprintf(stderr, "ssh-wrapper: askpass: server %q is not configured\n", alias)
		return 1
	}

	if len(args) > 0 && isConfirmationPrompt(args[0]) {
		fmt.Fprintln(stdout, "no")
		return 0
	}
	fmt.Fprintln(stdout, entry.Password)
	return 0
}

func runWrapperCommand(args []string, stdout, stderr io.Writer) int {
	switch args[0] {
	case "--wrapper-help":
		printHelp(stdout)
		return 0
	case "--wrapper-version":
		fmt.Fprintln(stdout, version)
		return 0
	case "--wrapper-list", "--wrapper-print-config", "--wrapper-install":
		if len(args) != 1 {
			fmt.Fprintf(stderr, "ssh-wrapper: %s does not accept arguments\n", args[0])
			return 2
		}
	default:
		fmt.Fprintf(stderr, "ssh-wrapper: unknown wrapper option %q\n", args[0])
		return 2
	}

	cfg, err := loadConfig(defaultConfigPath())
	if err != nil {
		fmt.Fprintf(stderr, "ssh-wrapper: %v\n", err)
		return 2
	}

	switch args[0] {
	case "--wrapper-list":
		for _, alias := range sortedAliases(cfg.Servers) {
			entry := cfg.Servers[alias]
			fmt.Fprintf(stdout, "%s\t%s@%s:%d\n", alias, entry.User, entry.hostName(), entry.Port)
		}
	case "--wrapper-print-config":
		_, _ = io.WriteString(stdout, renderSSHConfig(cfg.Servers))
	case "--wrapper-install":
		generatedPath, sshConfigPath, err := installSSHConfig(cfg)
		if err != nil {
			fmt.Fprintf(stderr, "ssh-wrapper: install SSH config: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "generated: %s\nOpenSSH config: %s\n", generatedPath, sshConfigPath)
	}
	return 0
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `ssh-wrapper/scp-wrapper are OpenSSH-compatible wrappers with per-host SSH_ASKPASS passwords.

Usage:
  ssh-wrapper [ssh arguments...]
  scp-wrapper [scp arguments...]
  ssh-wrapper --wrapper-list
  ssh-wrapper --wrapper-print-config
  ssh-wrapper --wrapper-install
  ssh-wrapper --wrapper-version
  ssh-wrapper --wrapper-help

Configuration is loaded from ssh-wrapper-config.toml beside the executable.
SSH_WRAPPER_CONFIG may override that path.
`)
}

func loadConfig(path string) (config, error) {
	var cfg config
	metadata, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return config{}, err
		}
		return config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	cfg.path = path
	if cfg.Servers == nil {
		cfg.Servers = make(map[string]server)
	}
	for alias, entry := range cfg.Servers {
		if err := validateServer(alias, entry, metadata.IsDefined("servers", alias, "password")); err != nil {
			return config{}, fmt.Errorf("config %q: %w", path, err)
		}
	}
	return cfg, nil
}

func validateServer(alias string, entry server, passwordDefined bool) error {
	if alias == "" || strings.ContainsAny(alias, " \t\r\n*?!") {
		return fmt.Errorf("invalid server alias %q; aliases must be literal OpenSSH Host names", alias)
	}
	if entry.Host != "" && entry.HostName != "" && entry.Host != entry.HostName {
		return fmt.Errorf("server %q has conflicting host and hostname values", alias)
	}
	if entry.hostName() == "" || strings.ContainsAny(entry.hostName(), " \t\r\n") {
		return fmt.Errorf("server %q has an invalid host", alias)
	}
	if entry.Port < 1 || entry.Port > 65535 {
		return fmt.Errorf("server %q has invalid port %d", alias, entry.Port)
	}
	if entry.User == "" || strings.ContainsAny(entry.User, " \t\r\n") {
		return fmt.Errorf("server %q has an invalid user", alias)
	}
	if !passwordDefined {
		return fmt.Errorf("server %q is missing password", alias)
	}
	for key, value := range entry.Options {
		if key == "" || strings.ContainsAny(key, " \t\r\n=") {
			return fmt.Errorf("server %q has invalid SSH option name %q", alias, key)
		}
		switch strings.ToLower(key) {
		case "password", "hostname", "port", "user":
			return fmt.Errorf("server %q SSH option %q must use its structured server field", alias, key)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("server %q SSH option %q contains a newline", alias, key)
		}
	}
	return nil
}

func defaultConfigPath() string {
	if path := os.Getenv(configEnvName); path != "" {
		if absolute, err := filepath.Abs(path); err == nil {
			return absolute
		}
		return path
	}
	executable, err := os.Executable()
	if err != nil {
		return configFileName
	}
	return filepath.Join(filepath.Dir(executable), configFileName)
}

func resolveFromConfig(configPath, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(filepath.Dir(configPath), value)
}

func findRealClient(client clientKind, configured string) (string, error) {
	if configured != "" {
		path, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("real %s %q was not found", client, configured)
		}
		if sameExecutable(path) {
			return "", fmt.Errorf("real %s %q resolves to the wrapper itself", client, path)
		}
		return path, nil
	}

	name := string(client)
	candidates := []string{"/usr/bin/" + name, "/usr/local/bin/" + name}
	if runtime.GOOS == "windows" {
		windowsDir := os.Getenv("WINDIR")
		if windowsDir == "" {
			windowsDir = `C:\Windows`
		}
		candidates = []string{filepath.Join(windowsDir, "System32", "OpenSSH", name+".exe")}
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && !sameExecutable(candidate) {
			return candidate, nil
		}
	}
	for _, candidateName := range []string{name, name + ".exe"} {
		if path, err := exec.LookPath(candidateName); err == nil && !sameExecutable(path) {
			return path, nil
		}
	}
	return "", fmt.Errorf("real OpenSSH %s client was not found; set [wrapper].real_%s", client, client)
}

func sameExecutable(path string) bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	a, errA := filepath.Abs(path)
	b, errB := filepath.Abs(self)
	if errA != nil || errB != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func executeClient(realClient string, args, env []string, stdout, stderr io.Writer) int {
	cmd := newCommand(realClient, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if code := exitErr.ExitCode(); code >= 0 {
				return code
			}
			return 255
		}
		fmt.Fprintf(stderr, "ssh-wrapper: start %q: %v\n", realClient, err)
		return 127
	}
	return 0
}

func configuredAlias(client clientKind, args []string, servers map[string]server) (string, bool, error) {
	if client == clientSSH {
		destination, ok := sshDestination(args)
		if !ok {
			return "", false, nil
		}
		alias := destinationAlias(destination)
		_, configured := servers[alias]
		return alias, configured, nil
	}

	aliases := scpRemoteAliases(args)
	if len(aliases) == 0 {
		return "", false, nil
	}
	configured := ""
	for _, alias := range aliases {
		if _, ok := servers[alias]; ok {
			configured = alias
			break
		}
	}
	if configured == "" {
		return "", false, nil
	}
	if len(aliases) > 1 {
		return "", false, fmt.Errorf("scp between different remote hosts is not supported when an automatic password is configured: %s", strings.Join(aliases, ", "))
	}
	return configured, true, nil
}

func configuredClientArgs(client clientKind, args []string, entry server, forcePassword bool) []string {
	if client == clientSCP {
		return configuredSCPArgs(args, entry, forcePassword)
	}
	return configuredSSHArgs(args, entry, forcePassword)
}

func configuredSSHArgs(args []string, entry server, forcePassword bool) []string {
	defaults := configuredDefaults(entry, forcePassword)
	destinationIndex := sshDestinationIndex(args)
	return insertArgs(args, optionInsertionIndex(args, destinationIndex), defaults)
}

func configuredSCPArgs(args []string, entry server, forcePassword bool) []string {
	explicitUser, explicitPort := scpOperandOverrides(args)
	if explicitUser {
		entry.User = ""
	}
	if explicitPort {
		entry.Port = 0
	}
	defaults := configuredDefaults(entry, forcePassword)
	operandIndex := scpFirstOperandIndex(args)
	return insertArgs(args, optionInsertionIndex(args, operandIndex), defaults)
}

func configuredDefaults(entry server, forcePassword bool) []string {
	defaults := []string{
		"-o", "HostName=" + entry.hostName(),
	}
	if entry.Port > 0 {
		defaults = append(defaults, "-o", "Port="+strconv.Itoa(entry.Port))
	}
	if entry.User != "" {
		defaults = append(defaults, "-o", "User="+entry.User)
	}
	if forcePassword {
		defaults = append(defaults,
			"-o", "BatchMode=no",
			"-o", "PreferredAuthentications=password,keyboard-interactive",
			"-o", "PubkeyAuthentication=no",
			"-o", "PasswordAuthentication=yes",
			"-o", "KbdInteractiveAuthentication=yes",
			"-o", "NumberOfPasswordPrompts=1",
		)
	}
	for _, key := range sortedOptionKeys(entry.Options) {
		defaults = append(defaults, "-o", key+"="+entry.Options[key])
	}
	return defaults
}

func insertArgs(args []string, index int, inserted []string) []string {
	if index < 0 {
		return args
	}
	result := make([]string, 0, len(args)+len(inserted))
	result = append(result, args[:index]...)
	result = append(result, inserted...)
	return append(result, args[index:]...)
}

func optionInsertionIndex(args []string, targetIndex int) int {
	if targetIndex > 0 && args[targetIndex-1] == "--" {
		return targetIndex - 1
	}
	return targetIndex
}

var sshOptionsWithArgument = map[byte]bool{
	'B': true, 'b': true, 'c': true, 'D': true, 'E': true, 'e': true,
	'F': true, 'I': true, 'i': true, 'J': true, 'L': true, 'l': true,
	'm': true, 'O': true, 'o': true, 'P': true, 'p': true, 'Q': true,
	'R': true, 'S': true, 'W': true, 'w': true,
}

func sshDestination(args []string) (string, bool) {
	index := sshDestinationIndex(args)
	if index < 0 {
		return "", false
	}
	return args[index], true
}

func sshDestinationIndex(args []string) int {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				return i + 1
			}
			return -1
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			return i
		}
		if shortOptionConsumesNext(arg, sshOptionsWithArgument) {
			i++
		}
	}
	return -1
}

func destinationAlias(destination string) string {
	if at := strings.LastIndex(destination, "@"); at >= 0 {
		return destination[at+1:]
	}
	return destination
}

var scpOptionsWithArgument = map[byte]bool{
	'c': true, 'D': true, 'F': true, 'i': true, 'J': true, 'l': true,
	'o': true, 'P': true, 'S': true, 'X': true,
}

func scpFirstOperandIndex(args []string) int {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				return i + 1
			}
			return -1
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			return i
		}
		if shortOptionConsumesNext(arg, scpOptionsWithArgument) {
			i++
		}
	}
	return -1
}

func shortOptionConsumesNext(arg string, optionsWithArgument map[byte]bool) bool {
	if len(arg) < 2 || arg[0] != '-' || arg == "--" {
		return false
	}
	for i := 1; i < len(arg); i++ {
		if optionsWithArgument[arg[i]] {
			return i == len(arg)-1
		}
	}
	return false
}

func scpRemoteAliases(args []string) []string {
	first := scpFirstOperandIndex(args)
	if first < 0 {
		return nil
	}
	seen := make(map[string]bool)
	var aliases []string
	for _, operand := range args[first:] {
		alias, ok := scpRemoteAlias(operand)
		if ok && !seen[alias] {
			seen[alias] = true
			aliases = append(aliases, alias)
		}
	}
	return aliases
}

func scpRemoteAlias(operand string) (string, bool) {
	alias, _, _, ok := scpRemoteSpec(operand)
	return alias, ok
}

func scpRemoteSpec(operand string) (alias string, explicitUser, explicitPort, ok bool) {
	if strings.HasPrefix(strings.ToLower(operand), "scp://") {
		parsed, err := url.Parse(operand)
		if err != nil || parsed.Hostname() == "" {
			return "", false, false, false
		}
		return parsed.Hostname(), parsed.User != nil && parsed.User.Username() != "", parsed.Port() != "", true
	}

	hostStart := 0
	if at := strings.IndexByte(operand, '@'); at >= 0 {
		hostStart = at + 1
	}
	if hostStart < len(operand) && operand[hostStart] == '[' {
		closingOffset := strings.IndexByte(operand[hostStart:], ']')
		closing := hostStart + closingOffset
		if closingOffset > 1 && len(operand) > closing+1 && operand[closing+1] == ':' {
			return operand[hostStart+1 : closing], hostStart > 0, false, true
		}
	}
	colon := strings.IndexByte(operand, ':')
	if colon <= 0 {
		return "", false, false, false
	}
	if strings.ContainsAny(operand[:colon], `/\`) {
		return "", false, false, false
	}
	if runtime.GOOS == "windows" && colon == 1 && isASCIIAlpha(operand[0]) {
		return "", false, false, false
	}
	host := operand[:colon]
	explicitUser = strings.Contains(host, "@")
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if host == "" {
		return "", false, false, false
	}
	return host, explicitUser, false, true
}

func scpOperandOverrides(args []string) (explicitUser, explicitPort bool) {
	first := scpFirstOperandIndex(args)
	if first < 0 {
		return false, false
	}
	for _, operand := range args[first:] {
		_, hasUser, hasPort, ok := scpRemoteSpec(operand)
		if ok {
			explicitUser = explicitUser || hasUser
			explicitPort = explicitPort || hasPort
		}
	}
	return explicitUser, explicitPort
}

func isASCIIAlpha(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func setEnv(env []string, key, value string) []string {
	result := make([]string, 0, len(env)+1)
	for _, item := range env {
		name, _, _ := strings.Cut(item, "=")
		matches := name == key
		if runtime.GOOS == "windows" {
			matches = strings.EqualFold(name, key)
		}
		if !matches {
			result = append(result, item)
		}
	}
	return append(result, key+"="+value)
}

func hasEnv(env []string, key string) bool {
	for _, item := range env {
		name, _, _ := strings.Cut(item, "=")
		if name == key || (runtime.GOOS == "windows" && strings.EqualFold(name, key)) {
			return true
		}
	}
	return false
}

func isConfirmationPrompt(prompt string) bool {
	lower := strings.ToLower(prompt)
	return strings.Contains(lower, "yes/no") || strings.Contains(lower, "confirm host key")
}

func sortedAliases(servers map[string]server) []string {
	aliases := make([]string, 0, len(servers))
	for alias := range servers {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	return aliases
}

func sortedOptionKeys(options map[string]string) []string {
	keys := make([]string, 0, len(options))
	for key := range options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func renderSSHConfig(servers map[string]server) string {
	var b strings.Builder
	b.WriteString("# Generated by ssh-wrapper. Passwords are intentionally omitted.\n")
	for _, alias := range sortedAliases(servers) {
		entry := servers[alias]
		fmt.Fprintf(&b, "\nHost %s\n", alias)
		fmt.Fprintf(&b, "  HostName %s\n", quoteSSHValue(entry.hostName()))
		fmt.Fprintf(&b, "  Port %d\n", entry.Port)
		fmt.Fprintf(&b, "  User %s\n", quoteSSHValue(entry.User))
		for _, key := range sortedOptionKeys(entry.Options) {
			fmt.Fprintf(&b, "  %s %s\n", key, quoteSSHValue(entry.Options[key]))
		}
	}
	return b.String()
}

func quoteSSHValue(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\"#") {
		return value
	}
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func installSSHConfig(cfg config) (string, string, error) {
	generatedPath := filepath.Join(filepath.Dir(cfg.path), generatedFileName)
	if err := os.WriteFile(generatedPath, []byte(renderSSHConfig(cfg.Servers)), 0o600); err != nil {
		return "", "", fmt.Errorf("write generated hosts %q: %w", generatedPath, err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("find home directory: %w", err)
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return "", "", fmt.Errorf("create %q: %w", sshDir, err)
	}
	sshConfigPath := filepath.Join(sshDir, "config")
	existing, err := os.ReadFile(sshConfigPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("read %q: %w", sshConfigPath, err)
	}

	includePath := filepath.ToSlash(generatedPath)
	includeLine := "Include " + quoteSSHValue(includePath)
	if !containsConfigLine(string(existing), includeLine) {
		updated := includeLine + "\n"
		if len(existing) > 0 {
			updated += string(existing)
		}
		mode := os.FileMode(0o600)
		if info, statErr := os.Stat(sshConfigPath); statErr == nil {
			mode = info.Mode().Perm()
		}
		if err := os.WriteFile(sshConfigPath, []byte(updated), mode); err != nil {
			return "", "", fmt.Errorf("write %q: %w", sshConfigPath, err)
		}
	}
	return generatedPath, sshConfigPath, nil
}

func containsConfigLine(content, line string) bool {
	for _, candidate := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(candidate) == line {
			return true
		}
	}
	return false
}
