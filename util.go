package main

import (
    "os"
	"fmt"

	"time"

	"github.com/bitrise-io/go-utils/command"
	"github.com/bitrise-io/go-utils/log"
)

func defaultGitBranch() (string, error) {
	cmd := command.New("bash", "-c", "git remote show origin | grep 'HEAD branch' | cut -d ':' -f 2")
    cmd.SetStderr(os.Stderr)
    log.Debugf("$ " + cmd.PrintableCommandArgs())

    output, err := cmd.RunAndReturnTrimmedOutput()
    if err != nil {
        return "", fmt.Errorf("failed to get default branch, error: %s", err)
    }

	return output, nil
}

func MergeCache(to_path string) error {
    startTime := time.Now()

    cmd := command.New("bash", "-c", "cat /tmp/buck-cache.part* > " + to_path)
    cmd.SetStdout(os.Stdout)
    cmd.SetStderr(os.Stderr)
    log.Debugf("$ " + cmd.PrintableCommandArgs())
    if err := cmd.Run(); err != nil {
        return fmt.Errorf("failed to merge archive, error: %s", err)
    }

    log.Donef("Merge split cache done in %s\n", time.Since(startTime))

	return nil
}

func Cat(path string) (string, error) {
	cmd := command.New("bash", "-c", "cat " + path)
    cmd.SetStderr(os.Stderr)
    log.Debugf("$ " + cmd.PrintableCommandArgs())

    output, err := cmd.RunAndReturnTrimmedOutput()
    if err != nil {
        return "", fmt.Errorf("failed to get default branch, error: %s", err)
    }

	return output, nil
}

func StoreCacheURL(url string) error {
    cmd := command.New("bash", "-c", "echo " + url + " > /Users/vagrant/deploy/cache.txt")

    cmd.SetStdout(os.Stdout)
    cmd.SetStderr(os.Stderr)
    log.Debugf("$ " + cmd.PrintableCommandArgs())
    if err := cmd.Run(); err != nil {
        return fmt.Errorf("failed to split archive, error: %s", err)
    }

    log.Donef("echo url success...\n")

    return nil
}