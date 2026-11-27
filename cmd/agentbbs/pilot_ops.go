package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/profullstack/agentbbs/internal/pilot"
)

func requireExistingPilotDatabase(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("pilot database does not exist; start the pilot before taking a backup")
		}
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("pilot database is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("pilot database permissions %04o are too broad", info.Mode().Perm())
	}
	return nil
}
func pilotBackup(cfg pilot.Config, args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: agentbbs pilot-backup <new-destination>")
		os.Exit(2)
	}
	if err := requireExistingPilotDatabase(cfg.DatabasePath); err != nil {
		fmt.Fprintln(os.Stderr, "pilot-backup:", err)
		os.Exit(1)
	}
	st, err := pilot.Open(cfg.DatabasePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pilot-backup:", err)
		os.Exit(1)
	}
	defer st.Close()
	if err := st.Backup(args[0]); err != nil {
		fmt.Fprintln(os.Stderr, "pilot-backup:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, args[0])
}

func pilotRestore(cfg pilot.Config, args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: agentbbs pilot-restore <verified-source>")
		os.Exit(2)
	}
	if err := pilot.Restore(args[0], cfg.DatabasePath); err != nil {
		fmt.Fprintln(os.Stderr, "pilot-restore:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, cfg.DatabasePath)
}
