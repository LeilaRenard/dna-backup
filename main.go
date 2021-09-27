package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/n-peugnet/dna-backup/logger"
)

type command struct {
	Flag  *flag.FlagSet
	Usage string
	Help  string
	Run   func([]string) error
}

const (
	name         = "dna-backup"
	baseUsage    = "<command> [<options>] [--] <args>"
	commitUsage  = "[<options>] [--] <source> <dest>"
	commitHelp   = "Create a new version of folder <source> into repo <dest>"
	restoreUsage = "[<options>] [--] <source> <dest>"
	restoreHelp  = "Restore the last version from repo <source> into folder <dest>"
)

var (
	logLevel    int
	commitCmd   = flag.NewFlagSet("commit", flag.ExitOnError)
	restoreCmd  = flag.NewFlagSet("restore", flag.ExitOnError)
	subcommands = map[string]command{
		commitCmd.Name():  {commitCmd, commitUsage, commitHelp, commitMain},
		restoreCmd.Name(): {restoreCmd, restoreUsage, restoreHelp, restoreMain},
	}
)

func init() {
	// init default help message
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: %s %s\n\ncommands:\n", name, baseUsage)
		for _, s := range subcommands {
			fmt.Printf("  %s	%s\n", s.Flag.Name(), s.Help)
		}
		os.Exit(1)
	}
	// setup subcommands
	for _, s := range subcommands {
		s.Flag.IntVar(&logLevel, "v", 3, "log verbosity level (0-4)")
	}
}

func main() {
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
	}
	cmd, exists := subcommands[args[0]]
	if !exists {
		fmt.Fprintf(flag.CommandLine.Output(), "error: unknown command %s\n\n", args[0])
		flag.Usage()
	}
	cmd.Flag.Usage = func() {
		fmt.Fprintf(cmd.Flag.Output(), "usage: %s %s %s\n\noptions:\n", name, cmd.Flag.Name(), cmd.Usage)
		cmd.Flag.PrintDefaults()
		os.Exit(1)
	}
	cmd.Flag.Parse(args[1:])
	logger.Init(logLevel)
	if err := cmd.Run(cmd.Flag.Args()); err != nil {
		fmt.Fprintf(cmd.Flag.Output(), "error: %s\n\n", err)
		cmd.Flag.Usage()
	}
}

func commitMain(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("wrong number of args")
	}
	source := args[0]
	dest := args[1]
	repo := NewRepo(dest)
	repo.Commit(source)
	return nil
}

func restoreMain(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("wrong number args")
	}
	source := args[0]
	dest := args[1]
	repo := NewRepo(source)
	repo.Restore(dest)
	return nil
}
