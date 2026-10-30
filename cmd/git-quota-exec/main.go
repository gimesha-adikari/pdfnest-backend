//go:build linux

// git-quota-exec is installed by an administrator as a root-owned ordinary
// executable. It has no setuid mode and performs no privileged provisioning.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"pdfnest-backend/internal/analyzer/engine/acquisition/gitquota"
)

func main() {
	if len(os.Args) == 3 && os.Args[1] == "authority" {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer cancel()
		if err := gitquota.ServeAuthority(ctx, os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "capability" {
		path := os.Getenv("ANALYZER_GIT_QUOTA_CONFIG")
		if len(os.Args) == 3 {
			path = os.Args[2]
		}
		manager, err := gitquota.Load(path)
		if err == nil {
			err = manager.Probe()
		}
		abi, abiErr := gitquota.ConfinementABI()
		result := map[string]any{"quotaStatus": "BLOCKED", "hardAllocatedBytes": gitquota.HardBytes, "hardTotalInodes": gitquota.HardInodes, "landlockABI": abi, "confinementRuntime": "NOT_EXERCISED"}
		if err != nil {
			result["reason"] = err.Error()
		} else {
			result["quotaStatus"] = "KERNEL_QUOTAS_VERIFIED"
		}
		if abiErr != nil {
			result["confinementReason"] = abiErr.Error()
		}
		json.NewEncoder(os.Stdout).Encode(result)
		if err != nil {
			os.Exit(125)
		}
		return
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "GIT_STORAGE_UNAVAILABLE:", err)
		os.Exit(125)
	}
}

func run(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: git-quota-exec authority CONFIG | probe CONFIG | run CONFIG SLOT TOKEN -- GIT_ARGS...")
	}
	manager, err := gitquota.Load(args[1])
	if err != nil {
		return err
	}
	if args[0] == "inspect" && len(args) == 3 {
		usage, err := manager.InspectLocal(args[2])
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(usage)
	}
	if args[0] != "confined" {
		if err := manager.ProbeLocal(); err != nil {
			return err
		}
	}
	if args[0] == "probe" && len(args) == 2 {
		if err := manager.Probe(); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"quotaStatus": "KERNEL_QUOTAS_VERIFIED", "confinementRuntime": "NOT_EXERCISED", "hardAllocatedBytes": gitquota.HardBytes, "hardTotalInodes": gitquota.HardInodes})
	}
	if args[0] == "run" {
		if len(args) < 6 || args[4] != "--" {
			return fmt.Errorf("run requires a protected authority lease token")
		}
		return manager.RunSupervised(args[2], args[3], args[5:])
	}
	if args[0] != "confined" || len(args) < 5 || args[3] != "--" {
		return fmt.Errorf("invalid helper invocation")
	}
	if !manager.Allows(args[2]) {
		return fmt.Errorf("unapproved slot")
	}
	if err := manager.CheckGit(); err != nil {
		return err
	}
	if err := gitquota.CheckNamespace(args[2]); err != nil {
		return err
	}
	if err := os.Chdir(filepath.Join(args[2], "home")); err != nil {
		return err
	}
	if err := gitquota.Confine(args[2]); err != nil {
		return err
	}
	return syscall.Exec("/usr/bin/git", append([]string{"git"}, args[4:]...), os.Environ())
}
