package startup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows"
)

type launchDependencies struct {
	arguments        []string
	environment      []string
	executable       func() (string, error)
	parentExecutable func() (string, error)
	lookPath         func(string) (string, error)
	start            func(string, []string, string, []string) error
	notify           func(string)
}

func RelaunchFromExplorer() (bool, error) {
	return relaunchFromExplorer(launchDependencies{
		arguments:        os.Args[1:],
		environment:      os.Environ(),
		executable:       os.Executable,
		parentExecutable: parentExecutable,
		lookPath:         exec.LookPath,
		start:            startProcess,
		notify:           notifyLaunchFailure,
	})
}

func relaunchFromExplorer(dependencies launchDependencies) (bool, error) {
	parent, err := dependencies.parentExecutable()
	if err != nil {
		return false, nil
	}
	environment := environmentMap(dependencies.environment)
	switch decideLaunch(parent, environment) {
	case continueLaunch:
		return false, nil
	case relaunchFailed:
		return false, failLaunch(dependencies.notify, errors.New("Windows Terminal opened, but Inlaid did not receive a terminal session"))
	}

	executable, err := dependencies.executable()
	if err != nil {
		return false, failLaunch(dependencies.notify, fmt.Errorf("locate Inlaid executable: %w", err))
	}
	terminal, err := dependencies.lookPath("wt.exe")
	if err != nil {
		return false, failLaunch(dependencies.notify, errors.New("Windows Terminal was not found. Install Windows Terminal, or run Inlaid from a terminal you already use"))
	}
	root := installRoot(executable)
	environmentList := appendWithoutKey(dependencies.environment, relaunchMarker)
	environmentList = appendWithoutKey(environmentList, launcherMarker)
	environmentList = append(environmentList, relaunchMarker+"=1")
	environmentList = append(environmentList, launcherMarker+"=direct")
	if err := dependencies.start(terminal, terminalArguments(executable, root, dependencies.arguments), root, environmentList); err != nil {
		return false, failLaunch(dependencies.notify, fmt.Errorf("start Windows Terminal: %w", err))
	}
	return true, nil
}

func failLaunch(notify func(string), err error) error {
	notify(err.Error())
	return err
}

func parentExecutable() (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(os.Getppid()))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(process)

	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer[:size]), nil
}

func startProcess(path string, arguments []string, root string, environment []string) error {
	command := exec.Command(path, arguments...)
	command.Dir = root
	command.Env = environment
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func notifyLaunchFailure(message string) {
	text, textErr := windows.UTF16PtrFromString(message)
	title, titleErr := windows.UTF16PtrFromString("Inlaid could not start")
	if textErr != nil || titleErr != nil {
		return
	}
	_, _ = windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONERROR)
}

func environmentMap(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[strings.ToUpper(name)] = value
		}
	}
	return values
}

func appendWithoutKey(environment []string, excluded string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(name, excluded) {
			continue
		}
		result = append(result, entry)
	}
	return result
}
