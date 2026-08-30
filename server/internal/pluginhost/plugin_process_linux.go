//go:build linux

package pluginhost

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

type childProcessSnapshot map[int]struct{}

func snapshotDirectChildProcesses() (childProcessSnapshot, error) {
	children, err := processChildren(os.Getpid())
	if err != nil {
		return nil, err
	}
	snapshot := make(childProcessSnapshot, len(children))
	for _, pid := range children {
		snapshot[pid] = struct{}{}
	}
	return snapshot, nil
}

func terminateNewChildProcessTrees(baseline childProcessSnapshot) error {
	children, err := processChildren(os.Getpid())
	if err != nil {
		return fmt.Errorf("list plugin processes: %w", err)
	}
	newRoots := make([]int, 0)
	for _, pid := range children {
		if _, existed := baseline[pid]; !existed {
			newRoots = append(newRoots, pid)
		}
	}
	if len(newRoots) == 0 {
		return errors.New("timed-out plugin process was not found")
	}

	all := make(map[int]struct{})
	for _, root := range newRoots {
		collectProcessTree(root, all)
	}
	pids := make([]int, 0, len(all))
	for pid := range all {
		pids = append(pids, pid)
	}
	// Descendants normally have larger PIDs. Killing in reverse order reduces
	// the chance of leaving a short-lived child behind before its parent dies.
	sort.Sort(sort.Reverse(sort.IntSlice(pids)))
	var killErrors []error
	for _, pid := range pids {
		process, findErr := os.FindProcess(pid)
		if findErr != nil {
			killErrors = append(killErrors, fmt.Errorf("find plugin process %d: %w", pid, findErr))
			continue
		}
		if killErr := process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) && !errors.Is(killErr, syscall.ESRCH) {
			killErrors = append(killErrors, fmt.Errorf("kill plugin process %d: %w", pid, killErr))
		}
	}
	return errors.Join(killErrors...)
}

func collectProcessTree(pid int, seen map[int]struct{}) {
	if pid <= 0 {
		return
	}
	if _, exists := seen[pid]; exists {
		return
	}
	seen[pid] = struct{}{}
	children, err := processChildren(pid)
	if err != nil {
		return
	}
	for _, child := range children {
		collectProcessTree(child, seen)
	}
}

// Linux records children per task, not only per process. Union every thread's
// children file so a child launched from a Go runtime worker thread is not
// missed.
func processChildren(pid int) ([]int, error) {
	taskPattern := filepath.Join("/proc", strconv.Itoa(pid), "task", "*", "children")
	files, err := filepath.Glob(taskPattern)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("process %d is unavailable", pid)
	}
	unique := make(map[int]struct{})
	for _, file := range files {
		data, readErr := os.ReadFile(file)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			return nil, readErr
		}
		for _, field := range strings.Fields(string(data)) {
			child, parseErr := strconv.Atoi(field)
			if parseErr == nil && child > 0 {
				unique[child] = struct{}{}
			}
		}
	}
	children := make([]int, 0, len(unique))
	for child := range unique {
		children = append(children, child)
	}
	sort.Ints(children)
	return children, nil
}
