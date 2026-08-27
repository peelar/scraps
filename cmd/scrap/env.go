package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

const envUsage = `usage: scrap env <command> [NAME...]

Approve which variables from Pi's startup environment may be sent to every
Scraps workspace. Only names are stored in the local mode-0600 profile;
values are read when Pi starts and remain available to all sandbox code.

Commands:
  allow NAME...  Approve one or more variable names
  deny NAME...   Revoke one or more approvals
  list           List approvals and whether each variable is currently set
  clear          Revoke every approval
`

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const maxApprovedEnvironmentNames = 64

var reservedEnvironmentNames = map[string]struct{}{
	"HOME": {}, "PATH": {}, "SHELL": {}, "TMPDIR": {},
	"SCRAP_WORKSPACE_ROOT": {}, "SCRAP_TOKEN": {}, "SCRAPS_CLIENT_CONFIG": {},
}

func runEnv(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, envUsage)
		return 2
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(envUsage)
		return 0
	}

	flags := flag.NewFlagSet("env "+args[0], flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(flags.Output(), envUsage) }
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	command, names := args[0], flags.Args()
	switch command {
	case "list":
		return runEnvList(names)
	case "clear":
		return runEnvClear(names)
	case "allow", "deny":
		return runEnvAllowDeny(command, names)
	default:
		fmt.Fprint(os.Stderr, envUsage)
		return 2
	}
}

func runEnvList(names []string) int {
	if len(names) != 0 {
		fmt.Fprint(os.Stderr, envUsage)
		return 2
	}
	profile, err := loadClientProfile(clientProfilePath())
	if err != nil {
		return envError(err)
	}
	normalized := normalizedEnvironmentNames(profile.EnvAllow)
	if len(normalized) == 0 {
		fmt.Println("No environment variables are approved. Scraps isolates Pi's local environment by default.")
		return 0
	}
	for _, name := range normalized {
		state := "unset"
		if _, ok := os.LookupEnv(name); ok {
			state = "set"
		}
		fmt.Printf("%s\t%s\n", name, state)
	}
	return 0
}

func runEnvClear(names []string) int {
	if len(names) != 0 {
		fmt.Fprint(os.Stderr, envUsage)
		return 2
	}
	profile, err := loadClientProfile(clientProfilePath())
	if err != nil {
		return envError(err)
	}
	profile.EnvAllow = nil
	if err := writeClientProfile(clientProfilePath(), profile); err != nil {
		return envError(err)
	}
	fmt.Println("Revoked all Scraps environment approvals. Restart Pi to apply the change.")
	return 0
}

func runEnvAllowDeny(command string, names []string) int {
	if len(names) == 0 {
		fmt.Fprint(os.Stderr, envUsage)
		return 2
	}
	for _, name := range names {
		if err := validateEnvironmentName(name); err != nil {
			return envError(err)
		}
	}
	profile, err := loadClientProfile(clientProfilePath())
	if err != nil {
		return envError(err)
	}
	approved := make(map[string]bool, len(profile.EnvAllow)+len(names))
	for _, name := range profile.EnvAllow {
		if validateEnvironmentName(name) == nil {
			approved[name] = true
		}
	}
	for _, name := range names {
		approved[name] = command == "allow"
	}
	profile.EnvAllow = profile.EnvAllow[:0]
	for name, allowed := range approved {
		if allowed {
			profile.EnvAllow = append(profile.EnvAllow, name)
		}
	}
	sort.Strings(profile.EnvAllow)
	if len(profile.EnvAllow) > maxApprovedEnvironmentNames {
		return envError(fmt.Errorf("at most %d environment variables may be approved", maxApprovedEnvironmentNames))
	}
	if err := writeClientProfile(clientProfilePath(), profile); err != nil {
		return envError(err)
	}
	verb := "Approved"
	if command == "deny" {
		verb = "Revoked"
	}
	fmt.Printf("%s %s for every Scraps workspace.\n", verb, strings.Join(names, ", "))
	if command == "allow" {
		printEnvAllowHints(names)
	} else {
		fmt.Println("Restart Pi to apply the change.")
	}
	return 0
}

// printEnvAllowHints reminds the user that values are read only at Pi startup
// and should come from a secret manager, never from the chat.
func printEnvAllowHints(names []string) {
	var unset []string
	for _, name := range names {
		if _, ok := os.LookupEnv(name); !ok {
			unset = append(unset, name)
		}
	}
	if len(unset) > 0 {
		fmt.Printf("Not set in this shell: %s.\n", strings.Join(unset, ", "))
	}
	fmt.Println("Scraps reads approved values only when Pi starts. Start or restart Pi through your secret manager, for example:")
	fmt.Println("  op run -- pi")
	fmt.Println("  doppler run -- pi")
	fmt.Println("  infisical run -- pi")
	fmt.Println("Do not paste secret values into Pi or chat.")
}

func validateEnvironmentName(name string) error {
	if !environmentNamePattern.MatchString(name) {
		return fmt.Errorf("invalid environment variable name %q", name)
	}
	if len(name) > 128 {
		return fmt.Errorf("environment variable name %q exceeds 128 bytes", name)
	}
	if _, reserved := reservedEnvironmentNames[name]; reserved || strings.HasPrefix(name, "OPENSHELL_") {
		return fmt.Errorf("environment variable %q is reserved by Scraps", name)
	}
	return nil
}

func normalizedEnvironmentNames(names []string) []string {
	unique := make(map[string]struct{}, len(names))
	for _, name := range names {
		if validateEnvironmentName(name) == nil {
			unique[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(unique))
	for name := range unique {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func envError(err error) int {
	fmt.Fprintf(os.Stderr, "scrap env: %v\n", err)
	return 1
}
