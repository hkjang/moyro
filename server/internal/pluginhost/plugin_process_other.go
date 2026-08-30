//go:build !linux

package pluginhost

import "errors"

type childProcessSnapshot map[int]struct{}

func snapshotDirectChildProcesses() (childProcessSnapshot, error) {
	return nil, errors.New("bounded Mattermost plugin activation requires Linux")
}

func terminateNewChildProcessTrees(childProcessSnapshot) error {
	return errors.New("bounded Mattermost plugin activation requires Linux")
}
